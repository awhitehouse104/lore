package mcpserver

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lore/internal/audit"
	"lore/internal/auth"
	"lore/internal/serverconfig"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPModernStatelessAuthenticationAndPerPrincipalDiscovery(t *testing.T) {
	queryToken := encodedHTTPToken("query")
	inspectToken := encodedHTTPToken("inspect")
	queryPrincipal := httpPrincipal(t, "query_reader", []auth.Permission{auth.PermissionQuery})
	inspectPrincipal := httpPrincipal(t, "index_reader", []auth.Permission{auth.PermissionInspect})
	config := testHTTPConfig([]auth.BearerPrincipal{
		bearerEntry(t, queryPrincipal, queryToken),
		bearerEntry(t, inspectPrincipal, inspectToken),
	})
	gateway := NewHTTPService(newTestService(t), config, slog.New(slog.DiscardHandler))

	querySession, queryRoundTripper := connectHTTPClient(t, gateway.Handler(), queryToken, "")
	if result := querySession.InitializeResult(); result == nil || result.ProtocolVersion != "2026-07-28" {
		t.Fatalf("modern negotiation result = %+v", result)
	}
	queryTools, err := querySession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(queryTools.Tools); strings.Join(got, ",") != "lore_page_references,lore_read,lore_search" {
		t.Fatalf("query tools = %v", got)
	}
	queryResources, err := querySession.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(queryResources.Resources) != 1 ||
		queryResources.Resources[0].URI != "lore://pages/page_project_foo" {
		t.Fatalf("query resources = %+v", queryResources.Resources)
	}
	if queryRoundTripper.sawSessionID {
		t.Fatal("stateless HTTP response exposed an MCP session ID")
	}

	inspectSession, _ := connectHTTPClient(t, gateway.Handler(), inspectToken, "")
	inspectTools, err := inspectSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := toolNames(inspectTools.Tools); strings.Join(got, ",") != "lore_index_status,lore_lint" {
		t.Fatalf("inspect tools = %v", got)
	}

	searchOutput := decodeOutput[SearchOutput](t, callTool(t, querySession, "lore_search", map[string]any{
		"query":   "deployable",
		"backend": "filesystem",
	}))
	if len(searchOutput.Results) != 1 || searchOutput.Results[0].ID != "page_project_foo" {
		t.Fatalf("authorized HTTP search = %+v", searchOutput)
	}
	localOnlyOutput := decodeOutput[SearchOutput](t, callTool(t, querySession, "lore_search", map[string]any{
		"query":   "reserved local clients",
		"backend": "filesystem",
	}))
	if len(localOnlyOutput.Results) != 0 {
		t.Fatalf("local-only knowledge crossed HTTP: %+v", localOnlyOutput.Results)
	}
}

func TestHTTPResourceReclassificationIsFreshAcrossRequests(t *testing.T) {
	service := newTestService(t)
	token := encodedHTTPToken("resources")
	principal := httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery})
	config := testHTTPConfig([]auth.BearerPrincipal{bearerEntry(t, principal, token)})
	session, _ := connectHTTPClient(t, NewHTTPService(service, config, slog.New(slog.DiscardHandler)).Handler(), token, "")

	before, err := session.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Resources) != 1 || before.Resources[0].URI != "lore://pages/page_project_foo" {
		t.Fatalf("resources before reclassification = %+v", before.Resources)
	}
	pagePath := filepath.Join(service.Repo.Root, "pages", "project-foo.md")
	data, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "sensitivity: normal", "sensitivity: sensitive", 1))
	if err := os.WriteFile(pagePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := session.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Resources) != 0 {
		t.Fatalf("resources after reclassification = %+v", after.Resources)
	}
	if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "lore://pages/page_project_foo"}); err == nil {
		t.Fatal("reclassified resource remained readable")
	}
}

func TestHTTPResourcePaginationAcrossStatelessRequests(t *testing.T) {
	service := newTestService(t)
	for index := 0; index < 100; index++ {
		data := fmt.Appendf(nil, `---
id: page_http_bulk_%03d
title: HTTP Bulk %03d
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
HTTP pagination page.
`, index, index)
		path := filepath.Join(service.Repo.Root, "pages", fmt.Sprintf("http-bulk-%03d.md", index))
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	token := encodedHTTPToken("pagination")
	principal := httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery})
	config := testHTTPConfig([]auth.BearerPrincipal{bearerEntry(t, principal, token)})
	session, _ := connectHTTPClient(t, NewHTTPService(service, config, slog.New(slog.DiscardHandler)).Handler(), token, "")

	first, err := session.ListResources(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Resources) != 100 || first.NextCursor == "" {
		t.Fatalf("first HTTP resource page = count %d cursor %q", len(first.Resources), first.NextCursor)
	}
	second, err := session.ListResources(t.Context(), &mcp.ListResourcesParams{Cursor: first.NextCursor})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Resources) != 1 || second.NextCursor != "" {
		t.Fatalf("second HTTP resource page = count %d cursor %q", len(second.Resources), second.NextCursor)
	}
	if first.Resources[len(first.Resources)-1].URI >= second.Resources[0].URI {
		t.Fatal("HTTP resource pagination order changed across stateless requests")
	}
}

func TestHTTPAuthenticationFailuresAreIndistinguishable(t *testing.T) {
	token := encodedHTTPToken("valid")
	config := testHTTPConfig([]auth.BearerPrincipal{bearerEntry(t, httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery}), token)})
	gateway := NewHTTPService(newTestService(t), config, slog.New(slog.DiscardHandler))
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocol-version":"2026-07-28"}}}`
	tests := []http.Header{
		{},
		{"Authorization": []string{"Basic " + token}},
		{"Authorization": []string{"Bearer " + encodedHTTPToken("wrong")}},
		{"Authorization": []string{"Bearer " + token, "Bearer " + token}},
		{"Authorization": []string{strings.Repeat("x", auth.MaximumAuthorizationHeaderBytes+1)}},
	}
	var firstBody string
	for index, header := range tests {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
		request.Header = header
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("case %d status = %d, body=%q", index, response.Code, response.Body.String())
		}
		if index == 0 {
			firstBody = response.Body.String()
		} else if response.Body.String() != firstBody {
			t.Fatalf("case %d body differs: %q != %q", index, response.Body.String(), firstBody)
		}
		if strings.Contains(response.Body.String(), token) {
			t.Fatal("authentication response leaked token")
		}
	}
}

func TestHTTPOriginBodyLimitsHealthAndPrivacyHeaders(t *testing.T) {
	token := encodedHTTPToken("origin")
	config := testHTTPConfig([]auth.BearerPrincipal{bearerEntry(t, httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery}), token)})
	config.NormalizedOrigins = []string{"https://allowed.example:443"}
	config.Transport.RequestMaxBytes = 1024
	gateway := NewHTTPService(newTestService(t), config, slog.New(slog.DiscardHandler))

	for _, test := range []struct {
		origin string
		status int
	}{
		{"", http.StatusBadRequest},
		{"https://allowed.example", http.StatusBadRequest},
		{"https://denied.example", http.StatusForbidden},
		{"null", http.StatusForbidden},
	} {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if test.origin != "" {
			request.Header.Set("Origin", test.origin)
		}
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		if response.Code != test.status {
			t.Errorf("origin %q status = %d, want %d; body=%q", test.origin, response.Code, test.status, response.Body.String())
		}
		if response.Header().Get("Cache-Control") != "private, no-store" {
			t.Errorf("origin %q cache control = %q", test.origin, response.Header().Get("Cache-Control"))
		}
	}

	large := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(strings.Repeat("x", 1025)))
	large.Header.Set("Authorization", "Bearer "+token)
	large.Header.Set("Content-Type", "application/json")
	large.Header.Set("Accept", "application/json, text/event-stream")
	response := httptest.NewRecorder()
	gateway.Handler().ServeHTTP(response, large)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, body=%q", response.Code, response.Body.String())
	}

	for _, path := range []string{"/health/live", "/health/ready"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != "{\"status\":\"ok\"}\n" {
			t.Fatalf("%s response = %d %q", path, response.Code, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "knowledge") ||
			strings.Contains(response.Body.String(), "remote_reader") ||
			strings.Contains(response.Body.String(), "commit") {
			t.Fatalf("%s leaked metadata: %q", path, response.Body.String())
		}
	}
	for _, path := range []string{"/mcp/extra", "/mcp?debug=true"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Errorf("non-exact path %q status = %d", path, response.Code)
		}
	}
}

func TestHTTPReadinessReportsRecoveryWithoutAffectingLiveness(t *testing.T) {
	service := newTestService(t)
	token := encodedHTTPToken("readiness")
	config := testHTTPConfig([]auth.BearerPrincipal{
		bearerEntry(t, httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery}), token),
	})
	gateway := NewHTTPService(service, config, slog.New(slog.DiscardHandler))

	assertHealthResponse(t, gateway.Handler(), "/health/live", http.StatusOK, "{\"status\":\"ok\"}\n")
	assertHealthResponse(t, gateway.Handler(), "/health/ready", http.StatusOK, "{\"status\":\"ok\"}\n")

	active := filepath.Join(service.Repo.Root, ".lore", "recovery", "active")
	if err := os.MkdirAll(active, 0o700); err != nil {
		t.Fatal(err)
	}
	assertHealthResponse(t, gateway.Handler(), "/health/live", http.StatusOK, "{\"status\":\"ok\"}\n")
	recoveryBody := assertHealthResponse(
		t,
		gateway.Handler(),
		"/health/ready",
		http.StatusServiceUnavailable,
		"{\"status\":\"unavailable\"}\n",
	)

	if err := os.Remove(active); err != nil {
		t.Fatal(err)
	}
	assertHealthResponse(t, gateway.Handler(), "/health/ready", http.StatusOK, "{\"status\":\"ok\"}\n")

	if err := os.Rename(
		filepath.Join(service.Repo.Root, "pages"),
		filepath.Join(service.Repo.Root, "pages-unavailable"),
	); err != nil {
		t.Fatal(err)
	}
	assertHealthResponse(t, gateway.Handler(), "/health/live", http.StatusOK, "{\"status\":\"ok\"}\n")
	degradedBody := assertHealthResponse(
		t,
		gateway.Handler(),
		"/health/ready",
		http.StatusServiceUnavailable,
		"{\"status\":\"unavailable\"}\n",
	)
	if recoveryBody != degradedBody {
		t.Fatalf("readiness failure bodies differ: %q != %q", recoveryBody, degradedBody)
	}
}

func TestHTTPReadinessRejectsUnsupportedMethodsBeforeCheckingRepository(t *testing.T) {
	checker := &countingReadinessChecker{}
	handler := exactRouteHandler("/mcp", http.NotFoundHandler(), checker)
	request := httptest.NewRequest(http.MethodPost, "/health/ready", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("unsupported readiness method = %d, headers=%v", response.Code, response.Header())
	}
	if checker.calls != 0 {
		t.Fatalf("unsupported readiness method ran %d repository checks", checker.calls)
	}
}

type countingReadinessChecker struct {
	calls int
}

func (c *countingReadinessChecker) Check() error {
	c.calls++
	return nil
}

func assertHealthResponse(t *testing.T, handler http.Handler, path string, status int, body string) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != status || response.Body.String() != body {
		t.Fatalf("%s response = %d %q, want %d %q", path, response.Code, response.Body.String(), status, body)
	}
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%s content type = %q", path, response.Header().Get("Content-Type"))
	}
	if response.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("%s cache control = %q", path, response.Header().Get("Cache-Control"))
	}
	for _, forbidden := range []string{
		"knowledge",
		"remote_reader",
		"recovery",
		"pages",
		"commit",
	} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("%s leaked metadata in %q", path, response.Body.String())
		}
	}
	return response.Body.String()
}

func TestConcurrencyTimeoutAndResponseLimitMiddleware(t *testing.T) {
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	blocking := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		entered <- struct{}{}
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})
	limited := concurrencyMiddleware(1, blocking)
	firstDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		limited.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
		firstDone <- response.Code
	}()
	<-entered
	second := httptest.NewRecorder()
	limited.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/", nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("concurrency rejection = %d, headers=%v", second.Code, second.Header())
	}
	close(release)
	if status := <-firstDone; status != http.StatusNoContent {
		t.Fatalf("first request status = %d", status)
	}

	cancelled := make(chan struct{})
	timeout := requestTimeoutMiddleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
		close(cancelled)
	}), 10*time.Millisecond)
	response := httptest.NewRecorder()
	timeout.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("timeout status = %d", response.Code)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("request timeout did not cancel handler context")
	}

	response = httptest.NewRecorder()
	responseLimitMiddleware(4, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "12345")
	})).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "12345") {
		t.Fatalf("response limit = %d %q", response.Code, response.Body.String())
	}
}

func TestResponseLimitRejectsStreaming(t *testing.T) {
	buffered := newBufferedResponseWriter(1024)
	if _, ok := any(buffered).(http.Flusher); ok {
		t.Fatal("buffered response writer must not advertise streaming support")
	}

	handler := responseLimitMiddleware(1024, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "data: private-event\n\n")
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("streaming response status = %d, want %d", response.Code, http.StatusInternalServerError)
	}
	if strings.Contains(response.Body.String(), "private-event") {
		t.Fatalf("streaming response body was published: %q", response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("streaming rejection content type = %q", contentType)
	}
	if cacheControl := response.Header().Get("Cache-Control"); cacheControl != "private, no-store" {
		t.Fatalf("streaming rejection cache control = %q", cacheControl)
	}
}

func TestBoundedRequestAppliesLimitBeforeTimeoutBuffer(t *testing.T) {
	writeResult := make(chan error, 1)
	handler := boundedRequestMiddleware(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, err := io.WriteString(writer, "12345")
		writeResult <- err
	}), time.Second, 4)

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/", nil))
	if err := <-writeResult; !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("upstream write error = %v, want %v", err, errResponseTooLarge)
	}
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "12345") {
		t.Fatalf("bounded request response = %d %q", response.Code, response.Body.String())
	}
}

func TestProtectedEndpointHoldsConcurrencySlotDuringResponsePublication(t *testing.T) {
	token := encodedHTTPToken("publication")
	principal := httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery})
	entries := []auth.BearerPrincipal{bearerEntry(t, principal, token)}
	config := testHTTPConfig(entries)
	config.Transport.MaxConcurrentRequests = 1
	config.Transport.RequestTimeout = serverconfig.Duration(5 * time.Second)
	config.Transport.ResponseMaxBytes = 1024

	entered := make(chan struct{}, 2)
	protocolHandler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, ok := auth.PrincipalFromContext(request.Context()); !ok || got.ID != principal.ID {
			t.Errorf("protocol principal = %+v, present=%t", got, ok)
		}
		entered <- struct{}{}
		_, _ = io.WriteString(writer, "bounded response")
	})
	handler := protectedEndpointHandler(
		config,
		auth.NewBearerAuthenticator(entries),
		audit.New(slog.New(slog.DiscardHandler)),
		newPrincipalRateLimiter(entries, config.Transport.RateLimit, time.Now),
		protocolHandler,
	)

	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseResponse := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseResponse)
	firstResponse := newBlockingResponseWriter(release)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		handler.ServeHTTP(firstResponse, authorizedRequest(token))
	}()

	select {
	case <-firstResponse.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("first response did not begin publication")
	}
	select {
	case <-entered:
	default:
		t.Fatal("first request published without entering the protocol handler")
	}

	secondResponse := httptest.NewRecorder()
	handler.ServeHTTP(secondResponse, authorizedRequest(token))
	if secondResponse.Code != http.StatusTooManyRequests || secondResponse.Header().Get("Retry-After") == "" {
		t.Fatalf("request during response publication = %d, headers=%v", secondResponse.Code, secondResponse.Header())
	}
	select {
	case <-entered:
		t.Fatal("second request entered the protocol handler while the first response was publishing")
	default:
	}

	releaseResponse()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish after response publication resumed")
	}
	if firstResponse.status != http.StatusOK {
		t.Fatalf("first response status = %d", firstResponse.status)
	}
}

func authorizedRequest(token string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

type blockingResponseWriter struct {
	header       http.Header
	status       int
	writeStarted chan struct{}
	release      <-chan struct{}
	writeOnce    sync.Once
}

func newBlockingResponseWriter(release <-chan struct{}) *blockingResponseWriter {
	return &blockingResponseWriter{
		header:       make(http.Header),
		writeStarted: make(chan struct{}),
		release:      release,
	}
}

func (w *blockingResponseWriter) Header() http.Header {
	return w.header
}

func (w *blockingResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *blockingResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.writeOnce.Do(func() { close(w.writeStarted) })
	<-w.release
	return len(data), nil
}

func TestHTTPServiceGracefulShutdownAllowsActiveRequest(t *testing.T) {
	config := testHTTPConfig(nil)
	config.Transport.ShutdownTimeout = serverconfig.Duration(time.Second)
	service := &HTTPService{config: config, logger: slog.New(slog.DiscardHandler)}
	entered := make(chan struct{})
	release := make(chan struct{})
	service.server = &http.Server{Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(entered)
		<-release
		writer.WriteHeader(http.StatusNoContent)
	})}
	clientConnection, serverConnection := net.Pipe()
	listener := newSingleConnectionListener(serverConnection)
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- service.Serve(ctx, listener) }()
	requestDone := make(chan error, 1)
	go func() {
		request, err := http.NewRequest(http.MethodGet, "http://lore.test/", nil)
		if err == nil {
			err = request.Write(clientConnection)
		}
		var response *http.Response
		if err == nil {
			response, err = http.ReadResponse(bufio.NewReader(clientConnection), request)
		}
		if err == nil {
			_ = response.Body.Close()
		}
		requestDone <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-serveDone:
		t.Fatalf("Serve returned before active request completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
	_ = clientConnection.Close()
}

type authorizationRoundTripper struct {
	base         http.RoundTripper
	token        string
	origin       string
	mu           sync.Mutex
	sawSessionID bool
}

func (t *authorizationRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	cloned.Header.Set("Authorization", "Bearer "+t.token)
	if t.origin != "" {
		cloned.Header.Set("Origin", t.origin)
	}
	response, err := t.base.RoundTrip(cloned)
	if err == nil && response.Header.Get("Mcp-Session-Id") != "" {
		t.mu.Lock()
		t.sawSessionID = true
		t.mu.Unlock()
	}
	return response, err
}

func connectHTTPClient(t *testing.T, handler http.Handler, token, origin string) (*mcp.ClientSession, *authorizationRoundTripper) {
	t.Helper()
	transport := &authorizationRoundTripper{
		base:   handlerRoundTripper{handler: handler},
		token:  token,
		origin: origin,
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "lore-http-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{
		Endpoint:             "http://lore.test/mcp",
		HTTPClient:           &http.Client{Transport: transport},
		DisableStandaloneSSE: true,
		MaxRetries:           -1,
	}, nil)
	if err != nil {
		t.Fatalf("HTTP client Connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, transport
}

func testHTTPConfig(entries []auth.BearerPrincipal) serverconfig.Config {
	config := serverconfig.Defaults()
	config.Repo = "/unused"
	config.BearerPrincipals = entries
	return config
}

func httpPrincipal(t *testing.T, id string, permissions []auth.Permission) auth.Principal {
	t.Helper()
	principal, err := auth.NewPrincipal(id, auth.TransportHTTP, permissions, []string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func bearerEntry(t *testing.T, principal auth.Principal, token string) auth.BearerPrincipal {
	t.Helper()
	decoded, err := auth.DecodeBearerToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return auth.BearerPrincipal{Principal: principal, Digest: auth.DigestBearerToken(decoded)}
}

func encodedHTTPToken(seed string) string {
	raw := strings.Repeat(seed, 32/len(seed)+1)[:32]
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func toolNames(tools []*mcp.Tool) []string {
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	return names
}

type handlerRoundTripper struct {
	handler http.Handler
}

func (t handlerRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	response := httptest.NewRecorder()
	t.handler.ServeHTTP(response, request)
	return response.Result(), nil
}

type singleConnectionListener struct {
	connection net.Conn
	closed     chan struct{}
	once       sync.Once
}

func newSingleConnectionListener(connection net.Conn) *singleConnectionListener {
	return &singleConnectionListener{connection: connection, closed: make(chan struct{})}
}

func (l *singleConnectionListener) Accept() (net.Conn, error) {
	var connection net.Conn
	l.once.Do(func() {
		connection = l.connection
	})
	if connection != nil {
		return connection, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleConnectionListener) Close() error {
	select {
	case <-l.closed:
	default:
		close(l.closed)
	}
	return nil
}

func (l *singleConnectionListener) Addr() net.Addr {
	return stringAddress("in-memory")
}

type stringAddress string

func (a stringAddress) Network() string { return "memory" }
func (a stringAddress) String() string  { return fmt.Sprintf("%s", string(a)) }
