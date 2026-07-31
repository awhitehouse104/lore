package mcpserver

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"lore/internal/auth"
	"lore/internal/serverconfig"
)

func TestPrincipalRateLimitIsolationRefillAndBoundedState(t *testing.T) {
	first := httpPrincipal(t, "first_reader", []auth.Permission{auth.PermissionQuery})
	second := httpPrincipal(t, "second_reader", []auth.Permission{auth.PermissionQuery})
	now := time.Unix(1_700_000_000, 0)
	limiter := newPrincipalRateLimiter(
		[]auth.BearerPrincipal{{Principal: first}, {Principal: second}},
		serverconfig.RateLimitConfig{RequestsPerMinute: 60, BurstRequests: 2},
		func() time.Time { return now },
	)
	var calls int
	handler := principalRateLimitMiddleware(limiter, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := func(principal auth.Principal) *http.Request {
		base := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		return base.WithContext(auth.WithPrincipal(base.Context(), principal))
	}

	for index := 0; index < 2; index++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(first))
		if response.Code != http.StatusNoContent {
			t.Fatalf("first principal request %d = %d", index, response.Code)
		}
	}
	limited := httptest.NewRecorder()
	handler.ServeHTTP(limited, request(first))
	if limited.Code != http.StatusTooManyRequests ||
		limited.Header().Get("Retry-After") != "1" ||
		limited.Body.String() != "Too Many Requests\n" {
		t.Fatalf("rate limit response = %d headers=%v body=%q", limited.Code, limited.Header(), limited.Body.String())
	}

	other := httptest.NewRecorder()
	handler.ServeHTTP(other, request(second))
	if other.Code != http.StatusNoContent {
		t.Fatalf("independent principal = %d", other.Code)
	}
	if len(limiter.buckets) != 2 {
		t.Fatalf("bucket count = %d", len(limiter.buckets))
	}

	unknownPrincipal := httpPrincipal(t, "unknown_reader", []auth.Permission{auth.PermissionQuery})
	unknown := httptest.NewRecorder()
	handler.ServeHTTP(unknown, request(unknownPrincipal))
	if unknown.Code != http.StatusInternalServerError || len(limiter.buckets) != 2 {
		t.Fatalf("unknown principal = %d, buckets=%d", unknown.Code, len(limiter.buckets))
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if missing.Code != http.StatusInternalServerError || len(limiter.buckets) != 2 {
		t.Fatalf("missing principal = %d, buckets=%d", missing.Code, len(limiter.buckets))
	}

	now = now.Add(time.Second)
	refilled := httptest.NewRecorder()
	handler.ServeHTTP(refilled, request(first))
	if refilled.Code != http.StatusNoContent {
		t.Fatalf("refilled request = %d", refilled.Code)
	}
	if calls != 4 {
		t.Fatalf("downstream calls = %d", calls)
	}
}

func TestPrincipalRateLimitDefaultBurstAndConcurrentSafety(t *testing.T) {
	principal := httpPrincipal(t, "burst_reader", []auth.Permission{auth.PermissionQuery})
	fixed := time.Unix(1_700_000_000, 0)
	limiter := newPrincipalRateLimiter(
		[]auth.BearerPrincipal{{Principal: principal}},
		serverconfig.RateLimitConfig{
			RequestsPerMinute: serverconfig.DefaultRateLimitRequestsPerMinute,
			BurstRequests:     serverconfig.DefaultRateLimitBurstRequests,
		},
		func() time.Time { return fixed },
	)
	handler := principalRateLimitMiddleware(limiter, http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	request := func() *http.Request {
		base := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		return base.WithContext(auth.WithPrincipal(base.Context(), principal))
	}

	for index := 0; index < serverconfig.DefaultRateLimitBurstRequests; index++ {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request())
		if response.Code != http.StatusNoContent {
			t.Fatalf("default burst request %d = %d", index, response.Code)
		}
	}
	exhausted := httptest.NewRecorder()
	handler.ServeHTTP(exhausted, request())
	if exhausted.Code != http.StatusTooManyRequests {
		t.Fatalf("request after default burst = %d", exhausted.Code)
	}

	concurrentLimiter := newPrincipalRateLimiter(
		[]auth.BearerPrincipal{{Principal: principal}},
		serverconfig.RateLimitConfig{RequestsPerMinute: 600, BurstRequests: 64},
		func() time.Time { return fixed },
	)
	concurrentHandler := principalRateLimitMiddleware(
		concurrentLimiter,
		http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusNoContent)
		}),
	)
	var accepted atomic.Int64
	var rejected atomic.Int64
	var wait sync.WaitGroup
	for range 128 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := httptest.NewRecorder()
			concurrentHandler.ServeHTTP(response, request())
			switch response.Code {
			case http.StatusNoContent:
				accepted.Add(1)
			case http.StatusTooManyRequests:
				rejected.Add(1)
			default:
				t.Errorf("concurrent response = %d", response.Code)
			}
		}()
	}
	wait.Wait()
	if accepted.Load() != 64 || rejected.Load() != 64 {
		t.Fatalf("concurrent accepted=%d rejected=%d", accepted.Load(), rejected.Load())
	}
}

func TestHTTPRateLimitExcludesAuthenticationFailuresAndHealth(t *testing.T) {
	token := encodedHTTPToken("rate-limit")
	principal := httpPrincipal(t, "remote_reader", []auth.Permission{auth.PermissionQuery})
	config := testHTTPConfig([]auth.BearerPrincipal{bearerEntry(t, principal, token)})
	config.Transport.MaxConcurrentRequests = 1
	config.Transport.RateLimit = serverconfig.RateLimitConfig{
		RequestsPerMinute: 60,
		BurstRequests:     2,
	}
	gateway := NewHTTPService(newTestService(t), config, slog.New(slog.DiscardHandler))
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"rate-test","version":"1"}}}`
	send := func(authorization string, origins ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initialize))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		if authorization != "" {
			request.Header.Set("Authorization", authorization)
		}
		for _, origin := range origins {
			request.Header.Add("Origin", origin)
		}
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		return response
	}

	for range 20 {
		if response := send("Bearer " + encodedHTTPToken("wrong")); response.Code != http.StatusUnauthorized {
			t.Fatalf("authentication failure = %d", response.Code)
		}
		if response := send("Bearer "+token, "https://rejected.example"); response.Code != http.StatusForbidden {
			t.Fatalf("origin rejection = %d", response.Code)
		}
		health := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(
			health,
			httptest.NewRequest(http.MethodGet, "/health/live", nil),
		)
		if health.Code != http.StatusOK {
			t.Fatalf("health response = %d", health.Code)
		}
	}

	for index := 0; index < 2; index++ {
		response := send("Bearer " + token)
		if response.Code != http.StatusOK {
			body, _ := io.ReadAll(response.Result().Body)
			t.Fatalf("authorized request %d = %d %q", index, response.Code, body)
		}
	}
	limited := send("Bearer " + token)
	if limited.Code != http.StatusTooManyRequests ||
		limited.Header().Get("Retry-After") != "1" ||
		limited.Header().Get("Cache-Control") != "private, no-store" ||
		limited.Body.String() != "Too Many Requests\n" {
		t.Fatalf("gateway rate limit = %d headers=%v body=%q", limited.Code, limited.Header(), limited.Body.String())
	}
	if strings.Contains(limited.Body.String(), token) || strings.Contains(limited.Body.String(), principal.ID) {
		t.Fatal("rate-limit response leaked authentication metadata")
	}
	if response := send("Bearer " + encodedHTTPToken("wrong")); response.Code != http.StatusUnauthorized {
		t.Fatalf("authentication after exhaustion = %d", response.Code)
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	tests := map[int]int{
		-1:  1,
		1:   60,
		2:   30,
		59:  2,
		60:  1,
		600: 1,
	}
	for requestsPerMinute, want := range tests {
		if got := retryAfterSeconds(requestsPerMinute); got != want {
			t.Errorf("retryAfterSeconds(%d) = %d, want %d", requestsPerMinute, got, want)
		}
	}
}
