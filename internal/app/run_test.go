package app

import (
	"bytes"
	"context"
	"encoding/json"
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
