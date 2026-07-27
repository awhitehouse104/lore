package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
