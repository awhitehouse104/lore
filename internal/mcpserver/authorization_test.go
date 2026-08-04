package mcpserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"lore/internal/auth"
	"lore/internal/core"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestPermissionFilteredDiscoveryAndSensitivityMasking(t *testing.T) {
	service := newTestService(t)
	principal := testPrincipal(t, "normal-reader", []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	session := connectTestClient(t, service, principal)
	list, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	if want := []string{"lore_page_references", "lore_read", "lore_search"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("query-only tools = %v, want %v", names, want)
	}

	searchOutput := decodeOutput[SearchOutput](t, callTool(t, session, "lore_search", map[string]any{
		"query":   "Notes material",
		"backend": "filesystem",
	}))
	for _, result := range searchOutput.Results {
		if result.ID == "page_sensitive_notes" || result.ID == "page_local_notes" {
			t.Fatalf("unauthorized search result = %+v", result)
		}
	}

	readResult, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lore_read",
		Arguments: map[string]any{"ref": "page_sensitive_notes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	errorText := toolResultText(t, readResult)
	if !readResult.IsError || !strings.Contains(errorText, `"code":"not_found"`) ||
		strings.Contains(errorText, "sensitive") || strings.Contains(errorText, "page_sensitive_notes") {
		t.Fatalf("masked read error = %s", errorText)
	}
	referencesResult, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lore_page_references",
		Arguments: map[string]any{"ref": "page_local_notes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	referencesError := toolResultText(t, referencesResult)
	if !referencesResult.IsError || !strings.Contains(referencesError, `"code":"not_found"`) ||
		strings.Contains(referencesError, "local-only") || strings.Contains(referencesError, "page_local_notes") {
		t.Fatalf("masked references error = %s", referencesError)
	}

	_, err = session.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lore_capture",
		Arguments: map[string]any{"kind": "note", "origin": "test", "text": "denied"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unregistered capture error = %v", err)
	}

	localPrincipal, err := auth.LocalProfile(auth.DefaultLocalProfile)
	if err != nil {
		t.Fatal(err)
	}
	localSession := connectTestClient(t, service, localPrincipal)
	localRead := decodeOutput[ReadOutput](t, callTool(t, localSession, "lore_read", map[string]any{"ref": "page_local_notes"}))
	if localRead.ID != "page_local_notes" || localRead.Sensitivity != "local-only" {
		t.Fatalf("authorized local-only read = %+v", localRead)
	}

	httpPrincipal, err := auth.NewPrincipal("remote-reader", auth.TransportHTTP, []auth.Permission{auth.PermissionQuery}, []string{"normal"})
	if err != nil {
		t.Fatal(err)
	}
	httpPrincipal.AllowedSensitivities["local-only"] = struct{}{}
	httpSession := connectTestClient(t, service, httpPrincipal)
	httpRead, err := httpSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_read", Arguments: map[string]any{"ref": "page_local_notes"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !httpRead.IsError || !strings.Contains(toolResultText(t, httpRead), `"code":"not_found"`) {
		t.Fatalf("defensive HTTP local-only read = %s", toolResultText(t, httpRead))
	}
}

func TestHistoryLintAndIndexDisclosureFollowSensitivity(t *testing.T) {
	service := newTestService(t)
	principal := testPrincipal(
		t,
		"limited-inspector",
		[]auth.Permission{auth.PermissionHistory, auth.PermissionInspect},
		[]string{"normal"},
	)
	session := connectTestClient(t, service, principal)
	recent := decodeOutput[RecentOutput](t, callTool(t, session, "lore_recent", map[string]any{"limit": 10}))
	if len(recent.Commits) != 1 || recent.Commits[0].Subject != "Lore knowledge changed" {
		t.Fatalf("filtered mixed history = %+v", recent.Commits)
	}
	indexStatus := decodeOutput[IndexStatusOutput](t, callTool(t, session, "lore_index_status", map[string]any{}))
	if indexStatus.CountsDisclosed || indexStatus.DocumentCount != nil ||
		indexStatus.PageCount != nil || indexStatus.SourceCount != nil {
		t.Fatalf("limited index disclosure = %+v", indexStatus)
	}

	invalidSensitive := []byte(`---
id: page_invalid_sensitive
title: Invalid Sensitive
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: invalid
sensitivity: sensitive
---
Hidden invalid material.
`)
	if err := os.WriteFile(filepath.Join(service.Repo.Root, "pages", "invalid-sensitive.md"), invalidSensitive, 0o644); err != nil {
		t.Fatal(err)
	}
	lintOutput := decodeOutput[LintOutput](t, callTool(t, session, "lore_lint", map[string]any{}))
	if !lintOutput.AdditionalInaccessibleErrors {
		t.Fatalf("lint did not report inaccessible aggregate: %+v", lintOutput)
	}
	for _, finding := range lintOutput.Findings {
		if strings.Contains(finding.Path, "invalid-sensitive") || strings.Contains(finding.Message, "Invalid Sensitive") {
			t.Fatalf("lint leaked inaccessible diagnostic: %+v", finding)
		}
	}
}

func TestCaptureIdempotencyAndSensitivity(t *testing.T) {
	service := newTestService(t)
	principal := testPrincipal(t, "normal-capture", []auth.Permission{auth.PermissionCapture}, []string{"normal"})
	session := connectTestClient(t, service, principal)
	missingSensitivity, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_capture",
		Arguments: map[string]any{
			"kind": "user_statement", "origin": "codex", "text": "Unclassified evidence.",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !missingSensitivity.IsError {
		t.Fatalf("capture without sensitivity succeeded: %+v", missingSensitivity)
	}
	input := map[string]any{
		"kind":            "user_statement",
		"origin":          "codex",
		"text":            "Exact captured evidence.",
		"sensitivity":     "normal",
		"idempotency_key": "capture-retry-1",
	}
	first := decodeOutput[CaptureOutput](t, callTool(t, session, "lore_capture", input))
	second := decodeOutput[CaptureOutput](t, callTool(t, session, "lore_capture", input))
	if first.ID == "" || second.ID != first.ID || !second.Replayed || second.RequestID == first.RequestID {
		t.Fatalf("capture replay first=%+v second=%+v", first, second)
	}
	matches, err := filepath.Glob(filepath.Join(service.Repo.Root, "sources", "*", "*", "*.md"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("captured source files = %v, err=%v", matches, err)
	}
	capturedData, err := os.ReadFile(filepath.Join(service.Repo.Root, filepath.FromSlash(first.Path)))
	if err != nil || !strings.HasSuffix(string(capturedData), input["text"].(string)) {
		t.Fatalf("captured source body err=%v data=%q", err, capturedData)
	}
	different := map[string]any{
		"kind":            "user_statement",
		"origin":          "codex",
		"text":            "Different evidence.",
		"sensitivity":     "normal",
		"idempotency_key": "capture-retry-1",
	}
	conflict, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "lore_capture", Arguments: different})
	if err != nil {
		t.Fatal(err)
	}
	if !conflict.IsError || !strings.Contains(toolResultText(t, conflict), `"code":"conflict"`) {
		t.Fatalf("capture conflict = %s", toolResultText(t, conflict))
	}
	denied, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_capture",
		Arguments: map[string]any{
			"kind": "user_statement", "origin": "codex", "text": "secret", "sensitivity": "sensitive",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !denied.IsError || !strings.Contains(toolResultText(t, denied), `"code":"permission_denied"`) {
		t.Fatalf("capture sensitivity denial = %s", toolResultText(t, denied))
	}
}

func TestTransactionOwnershipAuthorizationCommitReplayAndDiscard(t *testing.T) {
	service := newTestService(t)
	owner := testPrincipal(t, "curator-one", []auth.Permission{auth.PermissionCurate}, []string{"normal"})
	other := testPrincipal(t, "curator-two", []auth.Permission{auth.PermissionCurate}, []string{"normal"})
	ownerSession := connectTestClient(t, service, owner)
	otherSession := connectTestClient(t, service, other)

	createdContent := `---
id: page_mcp_created
title: MCP Created
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
Created through a bounded MCP transaction.
`
	clientActor, err := ownerSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"actor":          "attacker-selected",
			"message":        "create: rejected actor field",
			"operations": []any{map[string]any{
				"op": "create_page", "path": "pages/rejected.md", "content": createdContent,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !clientActor.IsError {
		t.Fatalf("client-selected actor unexpectedly succeeded: %+v", clientActor)
	}
	preview := decodeOutput[PreviewOutput](t, callTool(t, ownerSession, "lore_preview", map[string]any{
		"schema_version": 1,
		"message":        "create: add MCP page",
		"operations": []any{map[string]any{
			"op": "create_page", "path": "pages/mcp-created.md", "content": createdContent,
		}},
	}))
	if preview.TransactionID == "" || preview.PreviewDigest == "" || preview.DiffTruncated {
		t.Fatalf("preview = %+v", preview)
	}
	list := decodeOutput[TransactionListOutput](t, callTool(t, ownerSession, "lore_transaction_list", map[string]any{}))
	if len(list.Transactions) != 1 || list.Transactions[0].TransactionID != preview.TransactionID {
		t.Fatalf("owner list = %+v", list.Transactions)
	}
	otherList := decodeOutput[TransactionListOutput](t, callTool(t, otherSession, "lore_transaction_list", map[string]any{}))
	if len(otherList.Transactions) != 0 {
		t.Fatalf("other principal saw transactions: %+v", otherList.Transactions)
	}
	otherShow, err := otherSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_transaction_show", Arguments: map[string]any{"transaction_id": preview.TransactionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !otherShow.IsError || !strings.Contains(toolResultText(t, otherShow), `"code":"not_found"`) {
		t.Fatalf("ownership-masked show = %s", toolResultText(t, otherShow))
	}
	otherCommit, err := otherSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_commit",
		Arguments: map[string]any{
			"transaction_id": preview.TransactionID,
			"preview_digest": preview.PreviewDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !otherCommit.IsError || !strings.Contains(toolResultText(t, otherCommit), `"code":"not_found"`) {
		t.Fatalf("ownership-masked commit = %s", toolResultText(t, otherCommit))
	}

	commitArguments := map[string]any{
		"transaction_id":  preview.TransactionID,
		"preview_digest":  preview.PreviewDigest,
		"idempotency_key": "commit-retry-1",
	}
	committed := decodeOutput[CommitOutput](t, callTool(t, ownerSession, "lore_commit", commitArguments))
	if committed.Commit == "" || committed.AlreadyCommitted ||
		committed.TransactionState != "committed" {
		t.Fatalf("commit = %+v", committed)
	}
	replayed := decodeOutput[CommitOutput](t, callTool(t, ownerSession, "lore_commit", commitArguments))
	if replayed.Commit != committed.Commit || !replayed.AlreadyCommitted {
		t.Fatalf("commit replay = %+v", replayed)
	}
	if data, err := os.ReadFile(filepath.Join(service.Repo.Root, "pages", "mcp-created.md")); err != nil || string(data) != createdContent {
		t.Fatalf("committed content err=%v data=%q", err, data)
	}

	discardPreview := decodeOutput[PreviewOutput](t, callTool(t, ownerSession, "lore_preview", map[string]any{
		"schema_version": 1,
		"message":        "create: temporary MCP page",
		"operations": []any{map[string]any{
			"op": "create_page", "path": "pages/temporary.md",
			"content": strings.Replace(
				strings.Replace(createdContent, "page_mcp_created", "page_temporary", 1),
				"title: MCP Created", "title: Temporary MCP Page", 1,
			),
		}},
	}))
	discardArgs := map[string]any{"transaction_id": discardPreview.TransactionID}
	discarded := decodeOutput[TransactionDiscardOutput](t, callTool(t, ownerSession, "lore_transaction_discard", discardArgs))
	if !discarded.Discarded || discarded.TransactionState != "discarded" {
		t.Fatalf("discard = %+v", discarded)
	}
	replayedDiscard := decodeOutput[TransactionDiscardOutput](t, callTool(t, ownerSession, "lore_transaction_discard", discardArgs))
	if !replayedDiscard.Discarded || replayedDiscard.TransactionState != "discarded" {
		t.Fatalf("discard replay = %+v", replayedDiscard)
	}

	sensitivePreview, err := ownerSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "create: denied sensitive page",
			"operations": []any{map[string]any{
				"op": "create_page", "path": "pages/denied.md",
				"content": strings.Replace(createdContent, "sensitivity: normal", "sensitivity: sensitive", 1),
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sensitivePreview.IsError || !strings.Contains(toolResultText(t, sensitivePreview), `"code":"permission_denied"`) {
		t.Fatalf("sensitive preview denial = %s", toolResultText(t, sensitivePreview))
	}

	sensitivePage, err := service.Read(t.Context(), "page_sensitive_notes", nil)
	if err != nil {
		t.Fatal(err)
	}
	declassified := strings.Replace(sensitivePage.Content, "sensitivity: sensitive", "sensitivity: normal", 1)
	declassifyPreview, err := ownerSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "update: denied declassification",
			"operations": []any{map[string]any{
				"op": "update_page", "path": sensitivePage.Path,
				"expected_revision": sensitivePage.Revision, "content": declassified,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !declassifyPreview.IsError || !strings.Contains(toolResultText(t, declassifyPreview), `"code":"permission_denied"`) {
		t.Fatalf("current sensitivity denial = %s", toolResultText(t, declassifyPreview))
	}

	source, err := service.Read(t.Context(), "src_01ARZ3NDEKTSV4RRFFQ69G5FAV", nil)
	if err != nil {
		t.Fatal(err)
	}
	integrationPreview, err := ownerSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "integrate: denied sensitive target",
			"operations": []any{map[string]any{
				"op": "mark_source_integrated", "path": source.Path,
				"expected_revision": source.Revision, "page_ids": []string{"page_sensitive_notes"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !integrationPreview.IsError || !strings.Contains(toolResultText(t, integrationPreview), `"code":"permission_denied"`) {
		t.Fatalf("source integration sensitivity denial = %s", toolResultText(t, integrationPreview))
	}
	sourceSensitivityPreview, err := ownerSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "correct: denied source sensitivity",
			"operations": []any{map[string]any{
				"op": "set_source_sensitivity", "path": source.Path,
				"expected_revision": source.Revision, "sensitivity": "sensitive",
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sourceSensitivityPreview.IsError || !strings.Contains(toolResultText(t, sourceSensitivityPreview), `"code":"permission_denied"`) {
		t.Fatalf("source sensitivity authorization denial = %s", toolResultText(t, sourceSensitivityPreview))
	}

	normalPage, err := service.Read(t.Context(), "page_project_foo", nil)
	if err != nil {
		t.Fatal(err)
	}
	updateContent := normalPage.Content + "\nAuthorized update proposal.\n"
	reauthorizePreview := decodeOutput[PreviewOutput](t, callTool(t, ownerSession, "lore_preview", map[string]any{
		"schema_version": 1,
		"message":        "update: exercise commit authorization",
		"operations": []any{map[string]any{
			"op": "update_page", "path": normalPage.Path,
			"expected_revision": normalPage.Revision, "content": updateContent,
		}},
	}))
	reclassified := strings.Replace(normalPage.Content, "sensitivity: normal", "sensitivity: sensitive", 1)
	if err := os.WriteFile(filepath.Join(service.Repo.Root, filepath.FromSlash(normalPage.Path)), []byte(reclassified), 0o644); err != nil {
		t.Fatal(err)
	}
	reauthorizedCommit, err := ownerSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_commit",
		Arguments: map[string]any{
			"transaction_id": reauthorizePreview.TransactionID,
			"preview_digest": reauthorizePreview.PreviewDigest,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reauthorizedCommit.IsError || !strings.Contains(toolResultText(t, reauthorizedCommit), `"code":"not_found"`) {
		t.Fatalf("commit-time reauthorization = %s", toolResultText(t, reauthorizedCommit))
	}
}

func testPrincipal(t *testing.T, id string, permissions []auth.Permission, sensitivities []string) auth.Principal {
	t.Helper()
	principal, err := auth.NewPrincipal(id, auth.TransportStdio, permissions, sensitivities)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func connectTestClient(t *testing.T, service *core.Service, principal auth.Principal) *mcp.ClientSession {
	t.Helper()
	server := New(service, principal, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.ProtocolServer().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "authorization-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	return clientSession
}

func toolResultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("tool content = %+v", result.Content)
	}
	data, err := json.Marshal(result.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &content); err != nil {
		t.Fatal(err)
	}
	return content.Text
}
