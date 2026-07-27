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
