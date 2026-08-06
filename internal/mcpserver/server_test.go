package mcpserver

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"lore/internal/auth"
	"lore/internal/core"
	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"
	"lore/internal/search"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestModernProtocolListsAndCallsReadOnlyTools(t *testing.T) {
	service := newTestService(t)
	server := New(service, fullPrincipal(t), slog.New(slog.DiscardHandler))
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.ProtocolServer().Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "lore-test", Version: "1"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client Connect: %v", err)
	}
	t.Cleanup(func() { _ = clientSession.Close() })
	initializeResult := clientSession.InitializeResult()
	if initializeResult == nil || initializeResult.ProtocolVersion != "2026-07-28" {
		t.Fatalf("modern negotiation result = %+v", initializeResult)
	}
	for _, fragment := range []string{"lore_preflight", "one-fetch fast-forward", "self-contained", "Every capture requires an explicit", "shared facts", "living current view", "inspect page references", "repair every live synthesized-page backlink", "Immutable source-body links", "newly supplied integration ID", "existing IDs may outlive", "same actor and interface", "each MCP principal is separate", "relative dates", "known user timezone", "preferred name", "with the user's consent", "do not solicit unrelated personal defaults", "UTC metadata", "current UTC calendar date", "Lore tools for every repository operation they support", "tool arguments", "Never downgrade a known sensitivity without explicit trusted-user direction", "Idempotency keys are optional", "lore_read_many", "patch_page"} {
		if !strings.Contains(initializeResult.Instructions, fragment) {
			t.Errorf("server instructions missing %q: %q", fragment, initializeResult.Instructions)
		}
	}

	list, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if list.CacheScope != "private" || list.TTLMs != 0 {
		t.Fatalf("tool discovery cache metadata = scope %q ttl %d", list.CacheScope, list.TTLMs)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.InputSchema == nil || tool.OutputSchema == nil {
			t.Errorf("%s has incomplete schemas", tool.Name)
		}
		wantReadOnly, wantIdempotent, wantDestructive := expectedAnnotations(tool.Name)
		if tool.Annotations == nil ||
			tool.Annotations.ReadOnlyHint != wantReadOnly ||
			tool.Annotations.IdempotentHint != wantIdempotent {
			t.Errorf("%s annotations = %+v", tool.Name, tool.Annotations)
		}
		if tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != wantDestructive {
			t.Errorf("%s destructive annotation = %+v", tool.Name, tool.Annotations)
		}
		wantOpenWorld := tool.Name == "lore_preflight"
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint != wantOpenWorld {
			t.Errorf("%s open-world annotation = %+v", tool.Name, tool.Annotations)
		}
	}
	wantNames := []string{
		"lore_capture",
		"lore_commit",
		"lore_index_status",
		"lore_lint",
		"lore_page_references",
		"lore_preflight",
		"lore_preview",
		"lore_read",
		"lore_read_many",
		"lore_recent",
		"lore_search",
		"lore_transaction_discard",
		"lore_transaction_list",
		"lore_transaction_show",
	}
	if !reflect.DeepEqual(names, wantNames) {
		t.Fatalf("tool order = %v, want %v", names, wantNames)
	}

	searchResult := callTool(t, clientSession, "lore_search", map[string]any{
		"query":   "deployable Kubernetes",
		"types":   []string{"page"},
		"tags":    []string{"deployment"},
		"paths":   []string{"pages/"},
		"backend": "filesystem",
	})
	searchOutput := decodeOutput[SearchOutput](t, searchResult)
	if searchOutput.SchemaVersion != 1 || searchOutput.Status != "ok" ||
		!strings.HasPrefix(searchOutput.RequestID, "req_") ||
		searchOutput.Operation != "lore_search" ||
		len(searchOutput.Results) != 1 ||
		searchOutput.Results[0].ID != "page_project_foo" {
		t.Fatalf("search output = %+v", searchOutput)
	}
	if searchOutput.Matching != search.MatchingAuto || searchOutput.FuzzyExpanded {
		t.Fatalf("exact search matching metadata = %+v", searchOutput)
	}
	fuzzyOutput := decodeOutput[SearchOutput](t, callTool(t, clientSession, "lore_search", map[string]any{
		"query":    "deplyable Kubernets",
		"types":    []string{"page"},
		"matching": "auto",
		"backend":  "filesystem",
	}))
	if fuzzyOutput.Matching != search.MatchingAuto || !fuzzyOutput.FuzzyExpanded ||
		len(fuzzyOutput.Results) != 1 || len(fuzzyOutput.Results[0].FuzzyMatches) != 2 {
		t.Fatalf("fuzzy search output = %+v", fuzzyOutput)
	}

	readResult := callTool(t, clientSession, "lore_read", map[string]any{
		"ref":        searchOutput.Results[0].URI,
		"start_line": 1,
		"end_line":   12,
	})
	readOutput := decodeOutput[ReadOutput](t, readResult)
	if readOutput.ID != "page_project_foo" || readOutput.Sensitivity != "normal" ||
		!strings.Contains(readOutput.Content, "Project Foo") {
		t.Fatalf("read output = %+v", readOutput)
	}
	readManyOutput := decodeOutput[ReadManyOutput](t, callTool(t, clientSession, "lore_read_many", map[string]any{
		"items": []any{
			map[string]any{"ref": "page_project_foo", "start_line": 1, "end_line": 5},
			map[string]any{"ref": "page_sensitive_notes", "start_line": 1, "end_line": 4},
		},
	}))
	if readManyOutput.Operation != "lore_read_many" || len(readManyOutput.Documents) != 2 ||
		readManyOutput.Documents[0].ID != "page_project_foo" || readManyOutput.Documents[1].ID != "page_sensitive_notes" ||
		readManyOutput.TotalBytes != len(readManyOutput.Documents[0].Content)+len(readManyOutput.Documents[1].Content) {
		t.Fatalf("read-many output = %+v", readManyOutput)
	}
	referencesOutput := decodeOutput[PageReferencesOutput](t, callTool(t, clientSession, "lore_page_references", map[string]any{
		"ref": "page_project_foo",
	}))
	if referencesOutput.Operation != "lore_page_references" || referencesOutput.Target.ID != "page_project_foo" ||
		len(referencesOutput.LiveBacklinks) != 0 || len(referencesOutput.HistoricalSourceMentions) != 0 ||
		len(referencesOutput.SourceIntegrations) != 0 {
		t.Fatalf("page references output = %+v", referencesOutput)
	}

	recentOutput := decodeOutput[RecentOutput](t, callTool(t, clientSession, "lore_recent", map[string]any{"limit": 5}))
	if len(recentOutput.Commits) == 0 {
		t.Fatalf("recent output = %+v", recentOutput)
	}

	lintOutput := decodeOutput[LintOutput](t, callTool(t, clientSession, "lore_lint", map[string]any{}))
	if !lintOutput.Valid || lintOutput.Errors != 0 {
		t.Fatalf("lint output = %+v", lintOutput)
	}

	indexOutput := decodeOutput[IndexStatusOutput](t, callTool(t, clientSession, "lore_index_status", map[string]any{}))
	if indexOutput.IndexState != "missing" || indexOutput.Status != "ok" {
		t.Fatalf("index output = %+v", indexOutput)
	}

	invalid, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lore_search",
		Arguments: map[string]any{"query": "foo", "limit": maximumSearchLimit + 1},
	})
	if err != nil {
		t.Fatalf("invalid bounded CallTool: %v", err)
	}
	if !invalid.IsError || len(invalid.Content) != 1 {
		t.Fatalf("invalid bounded input result = %+v", invalid)
	}
	invalidMatching, err := clientSession.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "lore_search",
		Arguments: map[string]any{"query": "foo", "matching": "approximate"},
	})
	if err != nil {
		t.Fatalf("invalid matching CallTool: %v", err)
	}
	if !invalidMatching.IsError {
		t.Fatalf("invalid matching input result = %+v", invalidMatching)
	}
}

func TestMutationSchemasRequireSensitivityAndExposeSourceReclassification(t *testing.T) {
	captureSchema, err := json.Marshal(captureTool().InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(captureSchema, []byte(`"required":["kind","origin","text","sensitivity"]`)) ||
		bytes.Contains(captureSchema, []byte(`"default":"normal"`)) {
		t.Fatalf("capture schema = %s", captureSchema)
	}
	previewSchema, err := json.Marshal(previewTool().InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"patch_page", "replacements", "delete_page", "set_source_sensitivity", "allow_downgrade"} {
		if !bytes.Contains(previewSchema, []byte(fragment)) {
			t.Fatalf("preview schema missing %q: %s", fragment, previewSchema)
		}
	}
}

func TestPreflightToolReturnsStructuredBlockerAndIsLocalFullOnly(t *testing.T) {
	service := newTestService(t)
	if err := os.WriteFile(filepath.Join(service.Repo.Root, "preserve.md"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	localSession := connectTestClient(t, service, fullPrincipal(t))
	output := decodeOutput[PreflightOutput](t, callTool(t, localSession, "lore_preflight", map[string]any{}))
	if output.Status != "blocked" || output.Operation != "lore_preflight" || output.Result.Ready ||
		output.Result.Remote.Checked || len(output.Result.Blockers) != 1 || output.Result.Blockers[0].Code != "worktree_dirty" {
		t.Fatalf("preflight output = %+v", output)
	}

	remotePrincipal, err := auth.NewPrincipal(
		"remote_admin",
		auth.TransportHTTP,
		[]auth.Permission{auth.PermissionQuery, auth.PermissionCapture, auth.PermissionCurate, auth.PermissionInspect, auth.PermissionHistory},
		[]string{"normal", "sensitive"},
	)
	if err != nil {
		t.Fatal(err)
	}
	remoteSession := connectTestClient(t, service, remotePrincipal)
	tools, err := remoteSession.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "lore_preflight" {
			t.Fatal("HTTP principal was offered local preflight")
		}
	}
}

func TestPreviewDisclosesSafeUTCUpdateMinimum(t *testing.T) {
	service := newTestService(t)
	service.Clock = fixedTestClock{value: time.Date(2026, 7, 31, 21, 15, 0, 0, time.FixedZone("EDT", -4*60*60))}
	pagePath := filepath.Join(service.Repo.Root, "pages", "project-foo.md")
	current, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	proposed := strings.Replace(string(current), "Project Foo must remain deployable", "Project Foo remains deployable", 1)
	session := connectTestClient(t, service, fullPrincipal(t))
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "maintenance: test UTC update boundary",
			"operations": []any{map[string]any{
				"op":                "update_page",
				"path":              "pages/project-foo.md",
				"expected_revision": docs.Revision(current),
				"content":           proposed,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("preview result = %+v, want tool error", result)
	}
	var envelope externalError
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "invalid_argument" || envelope.Reason != "updated_too_old" ||
		envelope.Details["field"] != "updated" ||
		envelope.Details["minimum"] != "2026-08-01" ||
		envelope.Details["path"] != "pages/project-foo.md" {
		t.Fatalf("error envelope = %+v", envelope)
	}
	if strings.Contains(toolResultText(t, result), "Project Foo remains deployable") {
		t.Fatalf("error disclosed page body: %s", toolResultText(t, result))
	}
}

func TestPreviewDisclosesSafePatchMismatchWithoutEchoingText(t *testing.T) {
	service := newTestService(t)
	pagePath := filepath.Join(service.Repo.Root, "pages", "project-foo.md")
	current, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	secretOldText := "missing private patch sentinel"
	session := connectTestClient(t, service, fullPrincipal(t))
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "maintenance: test patch mismatch",
			"operations": []any{map[string]any{
				"op": "patch_page", "path": "pages/project-foo.md", "expected_revision": docs.Revision(current),
				"replacements": []any{map[string]any{"old": secretOldText, "new": "replacement"}},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("preview result = %+v, want tool error", result)
	}
	var envelope externalError
	text := toolResultText(t, result)
	if err := json.Unmarshal([]byte(text), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "invalid_argument" || envelope.Reason != "patch_text_not_found" ||
		envelope.Details["path"] != "pages/project-foo.md" || envelope.Details["replacement_index"] != float64(0) ||
		strings.Contains(text, secretOldText) {
		t.Fatalf("error envelope = %+v", envelope)
	}
}

func TestPreviewDisclosesMissingProspectiveIntegrationPageIDs(t *testing.T) {
	service := newTestService(t)
	sourcePath := "sources/2026/07/src_01ARZ3NDEKTSV4RRFFQ69G5FAV-evidence.md"
	sourceData, err := os.ReadFile(filepath.Join(service.Repo.Root, filepath.FromSlash(sourcePath)))
	if err != nil {
		t.Fatal(err)
	}
	session := connectTestClient(t, service, fullPrincipal(t))
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "lore_preview",
		Arguments: map[string]any{
			"schema_version": 1,
			"message":        "integrate: test missing page feedback",
			"operations": []any{map[string]any{
				"op":                "mark_source_integrated",
				"path":              sourcePath,
				"expected_revision": docs.Revision(sourceData),
				"page_ids":          []any{"page_z_missing", "page_a_missing"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("preview result = %+v, want tool error", result)
	}
	var envelope externalError
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "invalid_argument" || envelope.Reason != "integrated_page_missing" ||
		envelope.Details["field"] != "operations[].page_ids" ||
		!reflect.DeepEqual(envelope.Details["page_ids"], []any{"page_a_missing", "page_z_missing"}) {
		t.Fatalf("error envelope = %+v", envelope)
	}
	if strings.Contains(toolResultText(t, result), "Normal evidence for transaction authorization") {
		t.Fatalf("error disclosed source body: %s", toolResultText(t, result))
	}
}

func TestMappedToolErrorKeepsUnlistedValidationFailuresGeneric(t *testing.T) {
	apiErr := core.NewError(core.ExitValidation, "private_validation_state", "private validation detail")
	apiErr.Details = map[string]any{"path": "pages/private.md"}
	result, _ := mappedToolError(apiErr, "req_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	var envelope externalError
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "invalid_argument" || envelope.Reason != "" || envelope.Details != nil ||
		envelope.Message != "The tool arguments are invalid." {
		t.Fatalf("error envelope = %+v", envelope)
	}
	if strings.Contains(toolResultText(t, result), "private") {
		t.Fatalf("generic error disclosed private detail: %s", toolResultText(t, result))
	}
}

func TestMappedToolErrorRejectsMalformedIntegrationDisclosure(t *testing.T) {
	apiErr := core.NewError(core.ExitValidation, "integrated_page_missing", "private validation detail")
	apiErr.Details = map[string]any{"page_ids": []string{"not-a-page-id"}, "path": "pages/private.md"}
	result, _ := mappedToolError(apiErr, "req_01ARZ3NDEKTSV4RRFFQ69G5FAV")
	var envelope externalError
	if err := json.Unmarshal([]byte(toolResultText(t, result)), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Code != "invalid_argument" || envelope.Reason != "" || envelope.Details != nil ||
		envelope.Message != "The tool arguments are invalid." {
		t.Fatalf("error envelope = %+v", envelope)
	}
	if strings.Contains(toolResultText(t, result), "private") || strings.Contains(toolResultText(t, result), "not-a-page-id") {
		t.Fatalf("malformed disclosure leaked private detail: %s", toolResultText(t, result))
	}
}

func TestSafeWarningCodesDistinguishIndexHealthFromRefreshFailure(t *testing.T) {
	got := safeWarningCodes([]string{
		"index_permissions_open: .lore/index.sqlite: derived index permissions should deny group and other access",
		"existing index refresh failed; run lore index update",
		"uncommitted_source_change: sources/2026/07/unrelated.md: source has uncommitted Git status \" M\"",
	})
	want := []string{"index_health_warning", "index_refresh_failed", "source_worktree_dirty"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("safe warning codes = %v, want %v", got, want)
	}
}

func TestLegacyInitializeAndToolList(t *testing.T) {
	server := New(newTestService(t), fullPrincipal(t), slog.New(slog.DiscardHandler))
	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	serverSession, err := server.ProtocolServer().Connect(t.Context(), &mcp.IOTransport{
		Reader: clientToServerReader,
		Writer: serverToClientWriter,
	}, nil)
	if err != nil {
		t.Fatalf("server Connect: %v", err)
	}
	t.Cleanup(func() {
		_ = serverSession.Close()
		_ = clientToServerWriter.Close()
		_ = serverToClientReader.Close()
	})
	reader := bufio.NewReader(serverToClientReader)

	writeJSONLine(t, clientToServerWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "legacy-test", "version": "1"},
		},
	})
	initialize := readJSONLine(t, reader)
	result := initialize["result"].(map[string]any)
	if result["protocolVersion"] != "2025-11-25" {
		t.Fatalf("legacy initialize response = %#v", initialize)
	}
	writeJSONLine(t, clientToServerWriter, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
		"params":  map[string]any{},
	})
	writeJSONLine(t, clientToServerWriter, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	list := readJSONLine(t, reader)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 14 {
		t.Fatalf("legacy tool list = %#v", list)
	}
}

func expectedAnnotations(name string) (readOnly, idempotent, destructive bool) {
	switch name {
	case "lore_capture":
		return false, false, true
	case "lore_preview":
		return false, false, false
	case "lore_preflight":
		return false, true, false
	case "lore_commit", "lore_transaction_discard":
		return false, true, true
	default:
		return true, true, false
	}
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if result.IsError {
		data, _ := json.Marshal(result)
		t.Fatalf("CallTool(%s) tool error: %s", name, data)
	}
	if len(result.Content) != 1 {
		t.Fatalf("CallTool(%s) content = %+v", name, result.Content)
	}
	return result
}

func decodeOutput[T any](t *testing.T, result *mcp.CallToolResult) T {
	t.Helper()
	var output T
	data, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	if err := json.Unmarshal(data, &output); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	return output
}

func newTestService(t *testing.T) *core.Service {
	t.Helper()
	requireGit(t)
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(t.Context(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	page := []byte(`---
id: page_project_foo
title: Project Foo
kind: project
aliases: [foo]
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
tags: [deployment]
---
# Project Foo

Project Foo must remain deployable without Kubernetes.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "project-foo.md"), page, 0o644); err != nil {
		t.Fatal(err)
	}
	sensitive := []byte(`---
id: page_sensitive_notes
title: Sensitive Notes
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: sensitive
---
Private sensitive material.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "sensitive-notes.md"), sensitive, 0o644); err != nil {
		t.Fatal(err)
	}
	localOnly := []byte(`---
id: page_local_notes
title: Local Notes
kind: topic
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: local-only
---
Material reserved for local clients.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "local-notes.md"), localOnly, 0o644); err != nil {
		t.Fatal(err)
	}
	sourceBody := []byte("Normal evidence for transaction authorization.")
	source := docs.Source{
		ID:          "src_01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Kind:        "evidence",
		CapturedAt:  "2026-07-29T12:00:00Z",
		Origin:      "test",
		RawSHA256:   docs.SHA256(sourceBody),
		Sensitivity: "normal",
	}
	sourceData, err := docs.MarshalSource(source, sourceBody)
	if err != nil {
		t.Fatal(err)
	}
	sourceDir := filepath.Join(root, "sources", "2026", "07")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, source.ID+"-evidence.md"), sourceData, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "config", "user.name", "Lore Test")
	runGit(t, root, "config", "user.email", "lore-test@example.invalid")
	runGit(t, root, "add", "--", ".")
	runGit(t, root, "commit", "-m", "test: initialize Lore repository")
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	service := core.NewService(repo)
	service.Clock = fixedTestClock{value: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)}
	return service
}

type fixedTestClock struct {
	value time.Time
}

func (c fixedTestClock) Now() time.Time {
	return c.value
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for MCP integration tests")
	}
}

func fullPrincipal(t *testing.T) auth.Principal {
	t.Helper()
	principal, err := auth.LocalProfile(auth.DefaultLocalProfile)
	if err != nil {
		t.Fatal(err)
	}
	return principal
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.CommandContext(t.Context(), "git", arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func writeJSONLine(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(writer, "%s\n", data); err != nil {
		t.Fatal(err)
	}
}

func readJSONLine(t *testing.T, reader *bufio.Reader) map[string]any {
	t.Helper()
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read protocol response: %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(line, &value); err != nil {
		t.Fatalf("stdout contained non-protocol data %q: %v", line, err)
	}
	return value
}
