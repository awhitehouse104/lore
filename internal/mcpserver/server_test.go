package mcpserver

import (
	"bufio"
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

	"lore/internal/auth"
	"lore/internal/core"
	"lore/internal/docs"
	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"

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
	if result := clientSession.InitializeResult(); result == nil || result.ProtocolVersion != "2026-07-28" {
		t.Fatalf("modern negotiation result = %+v", result)
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
		if tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
			t.Errorf("%s open-world annotation = %+v", tool.Name, tool.Annotations)
		}
	}
	wantNames := []string{
		"lore_capture",
		"lore_commit",
		"lore_index_status",
		"lore_lint",
		"lore_preview",
		"lore_read",
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
	if len(tools) != 11 {
		t.Fatalf("legacy tool list = %#v", list)
	}
}

func expectedAnnotations(name string) (readOnly, idempotent, destructive bool) {
	switch name {
	case "lore_capture":
		return false, false, true
	case "lore_preview":
		return false, false, false
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
	return core.NewService(repo)
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
