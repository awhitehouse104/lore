package mcpserver

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	if got := toolNames(queryTools.Tools); strings.Join(got, ",") != "lore_read,lore_search" {
		t.Fatalf("query tools = %v", got)
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
