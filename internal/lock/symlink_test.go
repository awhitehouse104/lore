package lock

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestAcquireRejectsSymlinkRuntimeDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".lore")); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(root, "capture", time.Now()); err == nil {
		t.Fatal("Acquire unexpectedly accepted a symlink .lore directory")
	}
}
