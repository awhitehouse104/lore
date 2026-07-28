package gitx_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"lore/internal/gitx"
)

func TestCommandErrorDoesNotExposeCapturedOutput(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "fake-git")
	secret := "private source body sentinel"
	script := "#!/bin/sh\nprintf '%s\\n' '" + secret + "' >&2\nexit 1\n"
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := gitx.Client{Executable: executable}
	_, err := client.Head(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("fake Git command unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Git error exposed captured output: %v", err)
	}
}
