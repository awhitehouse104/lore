package lock

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContentionAndRelease(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lore"), 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	first, err := Acquire(root, "capture", now)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	_, err = Acquire(root, "capture", now.Add(time.Second))
	var contention *ContentionError
	if !errors.As(err, &contention) {
		t.Fatalf("second Acquire error = %T %v, want ContentionError", err, err)
	}
	if contention.Metadata.PID == 0 || contention.Metadata.Command != "capture" || !contention.Metadata.StartedAt.Equal(now) {
		t.Fatalf("contention metadata: %+v", contention.Metadata)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	second, err := Acquire(root, "capture", now.Add(time.Second))
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
