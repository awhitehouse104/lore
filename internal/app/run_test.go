package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"lore/internal/core"
	"lore/internal/repository"
	"lore/internal/search"
	"lore/internal/transaction"
)

func TestVersionJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"version", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run returned %d, stderr=%q", code, stderr.String())
	}

	var result struct {
		SchemaVersion int    `json:"schema_version"`
		Version       string `json:"version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if result.SchemaVersion != 1 || result.Version != "0.4.0-dev" {
		t.Fatalf("unexpected version response: %+v", result)
	}
}

func TestInitAndLintJSON(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"init", root, "--no-git", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("init returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--repo", root, "lint", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("lint returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	var result struct {
		SchemaVersion int  `json:"schema_version"`
		Valid         bool `json:"valid"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode lint JSON: %v", err)
	}
	if result.SchemaVersion != 1 || !result.Valid {
		t.Fatalf("unexpected lint response: %+v", result)
	}
}

func TestPreflightJSONBuildsIndexAndFailsClosedBeforeSyncWhenDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"init", root}, strings.NewReader(""), &stdout, &stderr); code != core.ExitOK {
		t.Fatalf("init returned %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(t.Context(), []string{"--repo", root, "preflight", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != core.ExitOK {
		t.Fatalf("local preflight returned %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var ready core.PreflightResult
	if err := json.Unmarshal(stdout.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	if !ready.Ready || ready.Scope != "local" || ready.RepositoryRoot != root || ready.Remote.Checked || ready.IndexAction != "built" {
		t.Fatalf("local preflight = %+v", ready)
	}

	if err := os.WriteFile(filepath.Join(root, "preserve.md"), []byte("uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(t.Context(), []string{"--repo", root, "preflight", "--sync", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != core.ExitConflict {
		t.Fatalf("dirty preflight returned %d, stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var blocked core.PreflightResult
	if err := json.Unmarshal(stdout.Bytes(), &blocked); err != nil {
		t.Fatal(err)
	}
	if blocked.Ready || blocked.Remote.Checked || len(blocked.Blockers) != 1 || blocked.Blockers[0].Code != "worktree_dirty" {
		t.Fatalf("dirty preflight = %+v", blocked)
	}
}

func TestMCPStdioKeepsStdoutProtocolClean(t *testing.T) {
	root := t.TempDir()
	var setupOut, setupErr bytes.Buffer
	if code := Run(t.Context(), []string{"init", root, "--no-git"}, strings.NewReader(""), &setupOut, &setupErr); code != 0 {
		t.Fatalf("init returned %d: %s", code, setupErr.String())
	}

	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	var stderr bytes.Buffer
	var wait sync.WaitGroup
	wait.Add(1)
	var code int
	go func() {
		defer wait.Done()
		code = Run(t.Context(), []string{"mcp", "stdio", "--repo", root, "--profile", "local-query"}, stdinReader, stdoutWriter, &stderr)
	}()
	responseReader := bufio.NewReader(stdoutReader)
	requests := []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"clean-stdout-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}
	for requestIndex, request := range requests {
		if _, err := fmt.Fprintln(stdinWriter, request); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(request, `"id":`) {
			line, err := responseReader.ReadBytes('\n')
			if err != nil {
				t.Fatalf("read MCP stdout: %v", err)
			}
			var response map[string]any
			if err := json.Unmarshal(line, &response); err != nil {
				t.Fatalf("MCP stdout line was not JSON-RPC: %q: %v", line, err)
			}
			if response["jsonrpc"] != "2.0" {
				t.Fatalf("MCP stdout line = %#v", response)
			}
			if requestIndex == 2 {
				tools := response["result"].(map[string]any)["tools"].([]any)
				if len(tools) != 4 {
					t.Fatalf("local-query advertised %d tools, want 4", len(tools))
				}
			}
		}
	}
	_ = stdinWriter.Close()
	wait.Wait()
	_ = stdoutWriter.Close()
	_ = stdoutReader.Close()
	if code != 0 {
		t.Fatalf("mcp stdio returned %d, stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected MCP stderr: %q", stderr.String())
	}
}

func TestMCPStdioStartupErrorsStayOffStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(t.Context(), []string{"--json", "mcp", "stdio", "--profile", "missing"}, strings.NewReader(""), &stdout, &stderr)
	if code != core.ExitUsage {
		t.Fatalf("mcp stdio returned %d, stderr=%q", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("startup error polluted protocol stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "unknown local MCP profile") {
		t.Fatalf("startup error missing from stderr: %q", stderr.String())
	}
}

func TestMCPCheckConfigLoadsExternalConfigurationAndTokens(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run(t.Context(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	token := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	tokenFile := filepath.Join(t.TempDir(), "reader.token")
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(t.TempDir(), "mcp.yaml")
	config := fmt.Sprintf(`version: 1
repo: %s
auth:
  tokens:
    - name: remote_reader
      token_file: %s
      permissions: [query]
      sensitivities: [normal]
`, root, tokenFile)
	if err := os.WriteFile(configFile, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := Run(t.Context(), []string{"mcp", "check-config", "--config", configFile}, strings.NewReader(""), &stdout, &stderr)
	if code != core.ExitOK || stdout.String() != "MCP server configuration is valid.\n" || stderr.Len() != 0 {
		t.Fatalf("check-config = code %d, stdout=%q, stderr=%q", code, stdout.String(), stderr.String())
	}

	stdout.Reset()
	code = Run(t.Context(), []string{"--json", "mcp", "check-config", "--config=" + configFile}, strings.NewReader(""), &stdout, &stderr)
	var result struct {
		SchemaVersion int    `json:"schema_version"`
		Status        string `json:"status"`
		Principals    int    `json:"principals"`
	}
	if code != core.ExitOK || json.Unmarshal(stdout.Bytes(), &result) != nil ||
		result.SchemaVersion != 1 || result.Status != "ok" || result.Principals != 1 {
		t.Fatalf("JSON check-config = code %d, stdout=%q, result=%+v", code, stdout.String(), result)
	}
}

func TestMCPHTTPCommandFlagAndConfigErrors(t *testing.T) {
	for _, arguments := range [][]string{
		{"mcp", "serve", "--config="},
		{"mcp", "check-config", "--unknown"},
		{"--repo", "/tmp/not-used", "mcp", "check-config"},
	} {
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), arguments, strings.NewReader(""), &stdout, &stderr); code != core.ExitUsage {
			t.Errorf("Run(%v) = %d, stderr=%q", arguments, code, stderr.String())
		}
	}
}

func TestCaptureJSONFromStdin(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	body := "exact\r\nUnicode 🦉"
	code := Run(
		context.Background(),
		[]string{"--repo", root, "capture", "--kind", "user_statement", "--origin", "codex", "--sensitivity", "normal", "--no-commit", "--json"},
		strings.NewReader(body),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("capture returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	var result struct {
		SchemaVersion int    `json:"schema_version"`
		Path          string `json:"path"`
		RawSHA256     string `json:"raw_sha256"`
		Bytes         int    `json:"bytes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode capture JSON: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(result.Path)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(data, []byte(body)) || result.Bytes != len([]byte(body)) {
		t.Fatalf("capture did not preserve body: result=%+v file=%q", result, data)
	}
}

func TestCaptureCLIRequiresExplicitSensitivity(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(
		context.Background(),
		[]string{"--repo", root, "capture", "--kind", "user_statement", "--origin", "codex", "--no-commit", "--json"},
		strings.NewReader("not written"),
		&stdout,
		&stderr,
	)
	if code != core.ExitUsage || !strings.Contains(stdout.String(), `"code":"missing_required_flag"`) ||
		!strings.Contains(stdout.String(), `requires --sensitivity`) {
		t.Fatalf("capture result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestSearchAndReadJSON(t *testing.T) {
	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	page := []byte(`---
id: page_project_foo
title: Project Foo
kind: project
aliases: [foo]
created: "2026-07-22"
updated: "2026-07-22"
status: active
sensitivity: normal
tags: [deployment]
---
# Summary

Project Foo should remain deployable without Kubernetes.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "project-foo.md"), page, 0o644); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	code := Run(context.Background(), []string{"search", "deploy", "Foo", "Kubernetes", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("search returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	var searched struct {
		SchemaVersion int `json:"schema_version"`
		Results       []struct {
			Path      string `json:"path"`
			LineStart int    `json:"line_start"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &searched); err != nil {
		t.Fatal(err)
	}
	if searched.SchemaVersion != 1 || len(searched.Results) != 1 || searched.Results[0].Path != "pages/project-foo.md" {
		t.Fatalf("search result: %+v", searched)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{
		"search", "deplyable", "Kubernets", "--matching", "auto", "--repo", root, "--json",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("fuzzy search returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	var fuzzy struct {
		Matching      string `json:"matching"`
		FuzzyExpanded bool   `json:"fuzzy_expanded"`
		Results       []struct {
			ID           string              `json:"id"`
			FuzzyMatches []search.FuzzyMatch `json:"fuzzy_matches"`
		} `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &fuzzy); err != nil {
		t.Fatal(err)
	}
	if fuzzy.Matching != "auto" || !fuzzy.FuzzyExpanded || len(fuzzy.Results) != 1 ||
		fuzzy.Results[0].ID != "page_project_foo" || len(fuzzy.Results[0].FuzzyMatches) != 2 {
		t.Fatalf("fuzzy search result: %+v", fuzzy)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--repo", root, "read", "foo", "--lines", "12:99", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("read returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	var read struct {
		SchemaVersion int    `json:"schema_version"`
		Path          string `json:"path"`
		Content       string `json:"content"`
		LineStart     int    `json:"line_start"`
		LineEnd       int    `json:"line_end"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.SchemaVersion != 1 || read.Path != "pages/project-foo.md" || read.LineStart != 12 || read.LineEnd != 14 || read.Content != "# Summary\n\nProject Foo should remain deployable without Kubernetes.\n" {
		t.Fatalf("read result: %+v", read)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--repo", root, "references", "page_project_foo", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("references returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
	}
	var references struct {
		SchemaVersion int `json:"schema_version"`
		Target        struct {
			Path string `json:"path"`
			ID   string `json:"id"`
		} `json:"target"`
		LiveBacklinks            []core.LinkReference              `json:"live_backlinks"`
		HistoricalSourceMentions []core.LinkReference              `json:"historical_source_mentions"`
		SourceIntegrations       []core.SourceIntegrationReference `json:"source_integrations"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &references); err != nil {
		t.Fatal(err)
	}
	if references.SchemaVersion != 1 || references.Target.Path != "pages/project-foo.md" ||
		references.Target.ID != "page_project_foo" || len(references.LiveBacklinks) != 0 ||
		len(references.HistoricalSourceMentions) != 0 || len(references.SourceIntegrations) != 0 {
		t.Fatalf("references result: %+v", references)
	}
}

func TestRecentJSON(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(
		context.Background(),
		[]string{"--repo", root, "capture", "--kind", "user_statement", "--origin", "test", "--sensitivity", "normal"},
		strings.NewReader("history source"),
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("capture returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"recent", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("recent returned %d: %s", code, stderr.String())
	}
	var result struct {
		SchemaVersion int `json:"schema_version"`
		Commits       []struct {
			Hash        string `json:"hash"`
			CommittedAt string `json:"committed_at"`
			Subject     string `json:"subject"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != 1 || len(result.Commits) < 1 || len(result.Commits[0].Hash) != 40 || !strings.HasPrefix(result.Commits[0].Subject, "capture: user_statement ") {
		t.Fatalf("recent result: %+v", result)
	}
	if _, err := time.Parse(time.RFC3339, result.Commits[0].CommittedAt); err != nil {
		t.Fatalf("recent timestamp %q: %v", result.Commits[0].CommittedAt, err)
	}
}

func TestLintReportsMissingAndInvalidConfig(t *testing.T) {
	tests := []struct {
		name     string
		config   []byte
		wantCode string
	}{
		{name: "missing", wantCode: "missing_config"},
		{name: "invalid", config: []byte("version: 1\nunknown: true\n"), wantCode: "invalid_config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.config != nil {
				if err := os.WriteFile(filepath.Join(root, "lore.yaml"), tt.config, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), []string{"--repo", root, "lint", "--json"}, strings.NewReader(""), &stdout, &stderr)
			if code != 1 {
				t.Fatalf("lint returned %d, stderr=%q, stdout=%q", code, stderr.String(), stdout.String())
			}
			var result struct {
				SchemaVersion int `json:"schema_version"`
				Findings      []struct {
					Code string `json:"code"`
				} `json:"findings"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.SchemaVersion != 1 || len(result.Findings) != 1 || result.Findings[0].Code != tt.wantCode {
				t.Fatalf("lint result: %+v", result)
			}
		})
	}
}

func TestPreviewAndTransactionInspectionJSON(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	page := `---
id: page_cli_preview
title: CLI Preview
kind: topic
created: "2026-07-28"
updated: "2026-07-28"
status: active
sensitivity: normal
---
Previewed from the CLI.
`
	requestBytes, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"message":        "create: CLI preview",
		"operations": []map[string]any{{
			"op": "create_page", "path": "pages/cli-preview.md", "content": page,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(
		context.Background(),
		[]string{"--repo", root, "preview", "--json"},
		bytes.NewReader(requestBytes),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("preview returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var preview struct {
		TransactionID string `json:"transaction_id"`
		PreviewDigest string `json:"preview_digest"`
		Status        string `json:"status"`
		Diff          string `json:"diff"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	if preview.TransactionID == "" || preview.PreviewDigest == "" || preview.Status != "previewed" ||
		!strings.Contains(preview.Diff, "+++ b/pages/cli-preview.md") {
		t.Fatalf("preview = %+v", preview)
	}
	if _, err := os.Stat(filepath.Join(root, "pages", "cli-preview.md")); !os.IsNotExist(err) {
		t.Fatalf("preview modified working tree: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"transaction", "list", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list returned %d: %s", code, stderr.String())
	}
	var listed struct {
		Transactions []struct {
			TransactionID string `json:"transaction_id"`
		} `json:"transactions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Transactions) != 1 || listed.Transactions[0].TransactionID != preview.TransactionID {
		t.Fatalf("list = %+v", listed)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"transaction", "show", preview.TransactionID, "--repo", root, "--diff", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("show returned %d: %s", code, stderr.String())
	}
	var shown struct {
		PreviewDigest string `json:"preview_digest"`
		Diff          string `json:"diff"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.PreviewDigest != preview.PreviewDigest || shown.Diff != preview.Diff {
		t.Fatalf("show = %+v", shown)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"transaction", "discard", preview.TransactionID, "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("discard returned %d: %s", code, stderr.String())
	}
	var discarded struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &discarded); err != nil {
		t.Fatal(err)
	}
	if discarded.Status != "discarded" {
		t.Fatalf("discard = %+v", discarded)
	}
}

func TestCommitTransactionJSON(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	request := `{"schema_version":1,"message":"create: CLI commit","operations":[{"op":"create_page","path":"pages/cli-commit.md","content":"---\nid: page_cli_commit\ntitle: CLI Commit\nkind: topic\ncreated: \"2026-07-28\"\nupdated: \"2026-07-28\"\nstatus: active\nsensitivity: normal\n---\nCommitted.\n"}]}`
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"preview", "--repo", root, "--json"}, strings.NewReader(request), &stdout, &stderr); code != 0 {
		t.Fatalf("preview returned %d: %s", code, stderr.String())
	}
	var preview struct {
		TransactionID string `json:"transaction_id"`
		PreviewDigest string `json:"preview_digest"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(context.Background(), []string{
		"commit", preview.TransactionID, "--preview-digest", preview.PreviewDigest,
		"--repo", root, "--no-push", "--json",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("commit returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var result struct {
		Status           string `json:"status"`
		Commit           string `json:"commit"`
		AlreadyCommitted bool   `json:"already_committed"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "committed" || len(result.Commit) != 40 || result.AlreadyCommitted {
		t.Fatalf("commit = %+v", result)
	}
	if _, err := os.Stat(filepath.Join(root, "pages", "cli-commit.md")); err != nil {
		t.Fatal(err)
	}
}

func TestTransactionPruneJSON(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	page := `---
id: page_cli_prune
title: CLI Prune
kind: topic
created: "2026-07-28"
updated: "2026-07-28"
status: active
sensitivity: normal
---
CLI prune fixture.
`
	requestBytes, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"message":        "create: CLI prune",
		"operations": []map[string]any{{
			"op": "create_page", "path": "pages/cli-prune.md", "content": page,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(
		context.Background(),
		[]string{"--repo", root, "preview", "--json"},
		bytes.NewReader(requestBytes),
		&stdout,
		&stderr,
	); code != core.ExitOK {
		t.Fatalf("preview returned %d: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var preview core.PreviewResult
	if err := json.Unmarshal(stdout.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(
		context.Background(),
		[]string{
			"--repo", root, "commit", preview.TransactionID,
			"--preview-digest", preview.PreviewDigest, "--json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != core.ExitOK {
		t.Fatalf("commit returned %d: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := transaction.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.LoadState(preview.TransactionID)
	if err != nil {
		t.Fatal(err)
	}
	state.CommittedAt = "2020-01-01T00:00:00Z"
	state.UpdatedAt = "2020-01-01T00:00:00Z"
	if err := store.UpdateState(preview.TransactionID, state); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(
		context.Background(),
		[]string{
			"--repo", root, "transaction", "prune",
			"--older-than", "1h", "--dry-run", "--json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != core.ExitOK {
		t.Fatalf("prune dry-run returned %d: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var dryRun core.TransactionPruneResult
	if err := json.Unmarshal(stdout.Bytes(), &dryRun); err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || dryRun.Selected != 1 || dryRun.Pruned != 0 {
		t.Fatalf("prune dry-run = %+v", dryRun)
	}

	stdout.Reset()
	stderr.Reset()
	if code := Run(
		context.Background(),
		[]string{
			"--repo", root, "transaction", "prune",
			"--older-than=1h", "--json",
		},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != core.ExitOK {
		t.Fatalf("prune returned %d: stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var pruned core.TransactionPruneResult
	if err := json.Unmarshal(stdout.Bytes(), &pruned); err != nil {
		t.Fatal(err)
	}
	if pruned.DryRun || pruned.Pruned != 1 || pruned.FilesRemoved != 3 ||
		len(pruned.Transactions) != 1 || pruned.Transactions[0].ArtifactState != "pruned" {
		t.Fatalf("prune result = %+v", pruned)
	}
}

func TestTransactionPruneAgeParsingAndRequiredFlag(t *testing.T) {
	tests := []struct {
		value string
		want  time.Duration
		ok    bool
	}{
		{value: "1h", want: time.Hour, ok: true},
		{value: "30d", want: 30 * 24 * time.Hour, ok: true},
		{value: "4w", want: 4 * 7 * 24 * time.Hour, ok: true},
		{value: "0d"},
		{value: "-1d"},
		{value: "1m"},
		{value: "1.5d"},
		{value: "forever"},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := parsePruneAge(test.value)
			if (err == nil) != test.ok || got != test.want {
				t.Fatalf("parsePruneAge(%q) = %s, %v", test.value, got, err)
			}
		})
	}

	var stdout, stderr bytes.Buffer
	code := Run(
		context.Background(),
		[]string{"transaction", "prune", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != core.ExitUsage || !strings.Contains(stdout.String(), `"code":"older_than_required"`) {
		t.Fatalf("missing older-than = code %d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRecoveryStatusJSONWithoutActiveJournal(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(context.Background(), []string{"recover", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("recover returned %d: %s", code, stderr.String())
	}
	var result struct {
		Active            bool   `json:"active"`
		RecommendedAction string `json:"recommended_action"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Active || result.RecommendedAction != "none" {
		t.Fatalf("result = %+v", result)
	}
}

func TestRecoveryFailuresPreserveExitCodesAndPrivateJSON(t *testing.T) {
	t.Run("actions without active recovery", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "knowledge")
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("init returned %d: %s", code, stderr.String())
		}
		for _, action := range []string{"--rollback", "--finalize"} {
			stdout.Reset()
			stderr.Reset()
			code := Run(
				t.Context(),
				[]string{"recover", "--repo", root, action, "--json"},
				strings.NewReader(""),
				&stdout,
				&stderr,
			)
			var envelope core.ErrorEnvelope
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("%s response is not JSON: %q: %v", action, stdout.String(), err)
			}
			if code != core.ExitUsage || envelope.Error == nil ||
				envelope.Error.Code != "no_active_recovery" || stderr.Len() != 0 {
				t.Fatalf("%s = code %d envelope=%+v stderr=%q", action, code, envelope, stderr.String())
			}
		}
	})

	t.Run("malformed journal", func(t *testing.T) {
		const secret = "seeded-secret-recovery-body"
		root := filepath.Join(t.TempDir(), "knowledge")
		var stdout, stderr bytes.Buffer
		if code := Run(t.Context(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("init returned %d: %s", code, stderr.String())
		}
		active := filepath.Join(root, ".lore", "recovery", "active")
		if err := os.MkdirAll(filepath.Join(active, "originals"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(active, "journal.json"),
			[]byte(`{"unexpected":"`+secret+`"}`+"\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}

		stdout.Reset()
		stderr.Reset()
		code := Run(
			t.Context(),
			[]string{"recover", "--repo", root, "--json"},
			strings.NewReader(""),
			&stdout,
			&stderr,
		)
		var envelope core.ErrorEnvelope
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("response is not JSON: %q: %v", stdout.String(), err)
		}
		if code != core.ExitRuntime || envelope.Error == nil ||
			envelope.Error.Code != "recovery_integrity_failed" || stderr.Len() != 0 {
			t.Fatalf("result = code %d envelope=%+v stderr=%q", code, envelope, stderr.String())
		}
		if strings.Contains(stdout.String(), secret) || strings.Contains(stderr.String(), secret) {
			t.Fatal("recovery failure output leaked journal content")
		}
	})
}

func TestIndexBuildAndStatusJSON(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root, "--no-git"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(context.Background(), []string{"index", "build", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("index build returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var build struct {
		SchemaVersion int    `json:"schema_version"`
		IndexState    string `json:"index_state"`
		Path          string `json:"path"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &build); err != nil {
		t.Fatal(err)
	}
	if build.SchemaVersion != 1 || build.IndexState != "uncertified" || build.Path != ".lore/index.sqlite" {
		t.Fatalf("build = %+v", build)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"--repo", root, "index", "status", "--verify", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("index status returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var status struct {
		IndexState      string `json:"index_state"`
		Verification    string `json:"verification"`
		ManifestMatches bool   `json:"manifest_matches"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.IndexState != "uncertified" || status.Verification != "full" || !status.ManifestMatches {
		t.Fatalf("status = %+v", status)
	}
	page := []byte(`---
id: page_index_cli
title: Index CLI
kind: note
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
Indexed through the CLI.
`)
	if err := os.WriteFile(filepath.Join(root, "pages", "index-cli.md"), page, 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"index", "update", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("index update returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var update struct {
		Added int `json:"added"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &update); err != nil {
		t.Fatal(err)
	}
	if update.Added != 1 {
		t.Fatalf("update = %+v", update)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"index", "clear", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("index clear returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var clear struct {
		Existed bool     `json:"existed"`
		Removed []string `json:"removed"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &clear); err != nil {
		t.Fatal(err)
	}
	if !clear.Existed || len(clear.Removed) == 0 {
		t.Fatalf("clear = %+v", clear)
	}
}

func TestSearchBackendSelectionAndStaleExplicitRefusal(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[user]\n\tname = Lore Test\n\temail = lore@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := filepath.Join(t.TempDir(), "knowledge")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"init", root}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("init returned %d: %s", code, stderr.String())
	}
	page := []byte(`---
id: page_indexed_search
title: Indexed Search
kind: note
created: "2026-07-29"
updated: "2026-07-29"
status: active
sensitivity: normal
---
Indexed evidence is deterministic.
`)
	pagePath := filepath.Join(root, "pages", "indexed-search.md")
	if err := os.WriteFile(pagePath, page, 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", root, "add", "--", "pages/indexed-search.md")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
	command = exec.Command("git", "-C", root, "commit", "-m", "test: add search fixture")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, output)
	}
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"index", "build", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("index build returned %d: %s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := Run(context.Background(), []string{
		"search", "indexed", "--backend", "index", "--include-sensitivity", "normal",
		"--repo", root, "--json",
	}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("indexed search returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var indexed struct {
		Backend    string `json:"backend"`
		IndexState string `json:"index_state"`
		Results    []any  `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &indexed); err != nil {
		t.Fatal(err)
	}
	if indexed.Backend != "index" || indexed.IndexState != "fresh" || len(indexed.Results) != 1 {
		t.Fatalf("indexed response = %+v", indexed)
	}

	if err := os.WriteFile(pagePath, append(page, []byte("External edit.\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"search", "indexed", "--backend", "auto", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("auto stale search returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var fallback struct {
		Backend    string `json:"backend"`
		IndexState string `json:"index_state"`
		Warnings   []any  `json:"warnings"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &fallback); err != nil {
		t.Fatal(err)
	}
	if fallback.Backend != "filesystem" || fallback.IndexState != "stale" || len(fallback.Warnings) == 0 {
		t.Fatalf("fallback response = %+v", fallback)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(context.Background(), []string{"search", "indexed", "--backend", "index", "--repo", root, "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 4 {
		t.Fatalf("explicit stale search returned %d: stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "index_not_fresh" {
		t.Fatalf("stale error = %+v", envelope)
	}
}

func TestJSONFlagProducesErrorEnvelopeRegardlessOfPosition(t *testing.T) {
	tests := [][]string{
		{"version", "--bad", "--json"},
		{"lint", "--bad", "--json"},
		{"preflight", "--bad", "--json"},
		{"capture", "--bad", "--json"},
		{"search", "--bad", "--json"},
		{"read", "--bad", "--json"},
		{"recent", "--bad", "--json"},
		{"preview", "--bad", "--json"},
		{"commit", "--bad", "--json"},
		{"transaction", "list", "--bad", "--json"},
		{"recover", "--bad", "--json"},
		{"index", "status", "--bad", "--json"},
		{"init", "--bad", "--json"},
	}
	for _, args := range tests {
		t.Run(args[0], func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
			if code != 2 {
				t.Fatalf("Run(%v) returned %d, stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
			}
			var envelope struct {
				SchemaVersion int `json:"schema_version"`
				Error         struct {
					Code    string         `json:"code"`
					Message string         `json:"message"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope %q: %v", stdout.String(), err)
			}
			if envelope.SchemaVersion != 1 || envelope.Error.Code != "unknown_flag" || envelope.Error.Message == "" || envelope.Error.Details == nil {
				t.Fatalf("error envelope: %+v", envelope)
			}
		})
	}
}

func TestEveryCommandSupportsHelp(t *testing.T) {
	for _, command := range []string{"init", "capture", "search", "read", "lint", "preflight", "preview", "commit", "transaction", "recover", "index", "recent", "version"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), []string{command, "--help"}, strings.NewReader(""), &stdout, &stderr)
			if code != 0 || stdout.Len() == 0 {
				t.Fatalf("%s --help returned %d, stdout=%q stderr=%q", command, code, stdout.String(), stderr.String())
			}
		})
	}
}
