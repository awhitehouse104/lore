package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if result.SchemaVersion != 1 || result.Version != "0.1.0-dev" {
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
		[]string{"--repo", root, "capture", "--kind", "user_statement", "--origin", "codex", "--no-commit", "--json"},
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
		[]string{"--repo", root, "capture", "--kind", "user_statement", "--origin", "test"},
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

func TestJSONFlagProducesErrorEnvelopeRegardlessOfPosition(t *testing.T) {
	tests := [][]string{
		{"version", "--bad", "--json"},
		{"lint", "--bad", "--json"},
		{"capture", "--bad", "--json"},
		{"search", "--bad", "--json"},
		{"read", "--bad", "--json"},
		{"recent", "--bad", "--json"},
		{"preview", "--bad", "--json"},
		{"commit", "--bad", "--json"},
		{"transaction", "list", "--bad", "--json"},
		{"recover", "--bad", "--json"},
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
	for _, command := range []string{"init", "capture", "search", "read", "lint", "preview", "commit", "transaction", "recover", "recent", "version"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(context.Background(), []string{command, "--help"}, strings.NewReader(""), &stdout, &stderr)
			if code != 0 || stdout.Len() == 0 {
				t.Fatalf("%s --help returned %d, stdout=%q stderr=%q", command, code, stdout.String(), stderr.String())
			}
		})
	}
}
