package mcpserver

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"lore/internal/auth"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAuditLogsExcludeSeededKnowledgeArgumentsAndCredentials(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := newTestService(t)
	fullClient := connectClientToServer(t, New(service, fullPrincipal(t), logger))

	secretQuery := "seeded-secret-search-query"
	secretCapture := "seeded secret capture body that must never enter logs"
	secretDiff := "seeded secret preview diff that must never enter logs"
	_ = callTool(t, fullClient, "lore_search", map[string]any{
		"query":   secretQuery,
		"backend": "filesystem",
	})
	_ = callTool(t, fullClient, "lore_capture", map[string]any{
		"kind":        "user_statement",
		"origin":      "audit_test",
		"text":        secretCapture,
		"sensitivity": "normal",
	})
	_ = callTool(t, fullClient, "lore_preview", map[string]any{
		"schema_version": 1,
		"message":        "create: audit secret",
		"operations": []any{map[string]any{
			"op":   "create_page",
			"path": "pages/audit-secret.md",
			"content": `---
id: page_audit_secret
title: Audit Secret
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
` + secretDiff + "\n",
		}},
	})

	normalReader := testPrincipal(t, "normal_reader", []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	readerClient := connectClientToServer(t, New(service, normalReader, logger))
	_, _ = readerClient.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lore_read",
		Arguments: map[string]any{"ref": "page_sensitive_notes"},
	})
	_, _ = readerClient.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: "lore://pages/page_sensitive_notes"})

	token := encodedHTTPToken("audit-token")
	config := testHTTPConfig([]auth.BearerPrincipal{
		bearerEntry(t, normalReader, token),
	})
	gateway := NewHTTPService(service, config, logger)
	denialBody := "seeded denied request body"
	peerAddress := "203.0.113.27:49152"
	forwardedIdentity := "seeded-forwarded-identity@example.com"
	sendDenied := func(authorization ...string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(denialBody))
		request.RemoteAddr = peerAddress
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		request.Header.Set("X-Forwarded-For", forwardedIdentity)
		request.Header.Set("Tailscale-User-Login", forwardedIdentity)
		for _, value := range authorization {
			request.Header.Add("Authorization", value)
		}
		response := httptest.NewRecorder()
		gateway.Handler().ServeHTTP(response, request)
		return response
	}
	invalid := sendDenied("Bearer " + token + "wrong")
	missing := sendDenied()
	malformed := sendDenied("Basic " + token)
	duplicated := sendDenied("Bearer "+token, "Bearer "+token)
	for name, response := range map[string]*httptest.ResponseRecorder{
		"duplicated": duplicated,
		"invalid":    invalid,
		"malformed":  malformed,
		"missing":    missing,
	} {
		if response.Code != http.StatusUnauthorized ||
			response.Header().Get("WWW-Authenticate") != "Bearer" ||
			response.Body.String() != "Unauthorized\n" {
			t.Fatalf("%s denial = %d headers=%v body=%q", name, response.Code, response.Header(), response.Body.String())
		}
	}

	logged := logs.String()
	for _, secret := range []string{
		secretQuery,
		secretCapture,
		secretDiff,
		token,
		"Sensitive Notes",
		"sensitive-notes.md",
		"Audit Secret",
		"audit-secret.md",
		denialBody,
		peerAddress,
		forwardedIdentity,
	} {
		if strings.Contains(logged, secret) {
			t.Fatalf("audit log leaked %q: %s", secret, logged)
		}
	}
	for _, expected := range []string{
		`"event":"mcp_operation"`,
		`"operation":"lore_search"`,
		`"operation":"lore_capture"`,
		`"operation":"lore_preview"`,
		`"operation":"resources/read"`,
		`"event":"mcp_authentication"`,
		`"reason":"missing_credentials"`,
		`"reason":"invalid_credentials"`,
	} {
		if !strings.Contains(logged, expected) {
			t.Errorf("audit log missing %s: %s", expected, logged)
		}
	}
	if strings.Count(logged, `"reason":"missing_credentials"`) != 1 ||
		strings.Count(logged, `"reason":"invalid_credentials"`) != 3 {
		t.Fatalf("authentication denial reasons = %s", logged)
	}
}

func TestAuditMiddlewareRecoversPanicsWithoutLoggingPanicContent(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	server := New(newTestService(t), fullPrincipal(t), logger)
	panicSecret := "seeded panic body"
	mcp.AddTool(server.ProtocolServer(), &mcp.Tool{Name: "lore_panic_test"}, func(
		context.Context,
		*mcp.CallToolRequest,
		struct{},
	) (*mcp.CallToolResult, struct{}, error) {
		panic(panicSecret)
	})
	client := connectClientToServer(t, server)
	_, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "lore_panic_test", Arguments: map[string]any{}})
	if err == nil || strings.Contains(err.Error(), panicSecret) {
		t.Fatalf("panic CallTool error = %v", err)
	}
	if strings.Contains(logs.String(), panicSecret) ||
		!strings.Contains(logs.String(), `"outcome":"panic"`) ||
		!strings.Contains(logs.String(), `"error_id":"err_`) {
		t.Fatalf("panic audit log = %s", logs.String())
	}
}

func connectClientToServer(t *testing.T, server *Server) *mcp.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.ProtocolServer().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "lore-audit-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}
