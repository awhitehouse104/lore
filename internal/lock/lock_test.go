package lock

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

var crashHelperHandle *Handle

func TestContentionReportsOwnerAndAcquiresAfterRelease(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	first, err := Acquire(context.Background(), root, "capture", now, 0)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}

	_, err = Acquire(context.Background(), root, "preview", now.Add(time.Second), 25*time.Millisecond)
	var contention *ContentionError
	if !errors.As(err, &contention) {
		t.Fatalf("second Acquire error = %T %v, want ContentionError", err, err)
	}
	if !contention.MetadataAvailable || contention.Metadata.PID != os.Getpid() ||
		contention.Metadata.Command != "capture" || !contention.Metadata.StartedAt.Equal(now) {
		t.Fatalf("contention metadata: %+v", contention)
	}
	if contention.LegacyDirectory || contention.Waited < 20*time.Millisecond {
		t.Fatalf("contention timing/type: %+v", contention)
	}

	released := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		released <- first.Release()
	}()
	second, err := Acquire(context.Background(), root, "preview", now.Add(time.Second), time.Second)
	if err != nil {
		t.Fatalf("Acquire waiting for release: %v", err)
	}
	if err := <-released; err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("second Release: %v", err)
	}

	info, err := os.Lstat(Path(root))
	if err != nil {
		t.Fatalf("persistent lock file missing: %v", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("lock mode = %v, want regular 0600", info.Mode())
	}
	if err := os.Mkdir(Path(root), 0o700); !errors.Is(err, os.ErrExist) {
		t.Fatalf("legacy mkdir after upgrade error = %v, want exists", err)
	}
}

func TestContextCancellationInterruptsContentionWait(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	first, err := Acquire(context.Background(), root, "capture", now, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err = Acquire(ctx, root, "preview", now, time.Second)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Acquire error = %T %v, want context deadline", err, err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestProcessDeathReleasesLock(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("repository flock implementation is Linux-specific")
	}
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "acquired")
	command := exec.Command(os.Args[0], "-test.run=^TestLockCrashHelper$")
	command.Env = append(os.Environ(),
		"LORE_LOCK_CRASH_ROOT="+root,
		"LORE_LOCK_CRASH_MARKER="+marker,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	waitForFile(t, marker, 5*time.Second)
	if err := command.Process.Kill(); err != nil {
		t.Fatalf("kill helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed helper exited successfully")
	}

	handle, err := Acquire(
		context.Background(),
		root,
		"after-crash",
		time.Date(2026, 7, 22, 1, 2, 4, 0, time.UTC),
		500*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("Acquire after process death: %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestLockCrashHelper(t *testing.T) {
	root := os.Getenv("LORE_LOCK_CRASH_ROOT")
	if root == "" {
		return
	}
	var err error
	crashHelperHandle, err = Acquire(
		context.Background(),
		root,
		"crash-helper",
		time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("LORE_LOCK_CRASH_MARKER"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {}
}

func TestLegacyDirectoryCanFinishDuringRetry(t *testing.T) {
	root := t.TempDir()
	lockDir := Path(root)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC)
	writeLegacyMetadata(t, lockDir, now)

	_, err := Acquire(context.Background(), root, "preview", now, 0)
	var contention *ContentionError
	if !errors.As(err, &contention) || !contention.LegacyDirectory ||
		!contention.MetadataAvailable || contention.Metadata.Command != "legacy-writer" {
		t.Fatalf("legacy contention = %#v, err = %v", contention, err)
	}

	removed := make(chan error, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.Remove(filepath.Join(lockDir, "metadata.json")); err != nil {
			removed <- err
			return
		}
		removed <- os.Remove(lockDir)
	}()
	handle, err := Acquire(context.Background(), root, "preview", now.Add(time.Second), time.Second)
	if err != nil {
		t.Fatalf("Acquire after legacy release: %v", err)
	}
	if err := <-removed; err != nil {
		t.Fatalf("remove legacy lock: %v", err)
	}
	if err := handle.Release(); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(lockDir); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("migrated lock = %v, %v; want regular file", info, err)
	}
}

func TestMalformedLegacyDirectoryFailsClosed(t *testing.T) {
	root := t.TempDir()
	lockDir := Path(root)
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "metadata.json"), []byte(`{"pid":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Acquire(
		context.Background(),
		root,
		"preview",
		time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		0,
	)
	var contention *ContentionError
	if !errors.As(err, &contention) || !contention.LegacyDirectory || contention.MetadataAvailable {
		t.Fatalf("contention = %#v, err = %v", contention, err)
	}
	if contention.Cause == nil {
		t.Fatal("malformed metadata cause is unavailable")
	}
}

func TestMalformedCurrentMetadataFailsClosedDuringContention(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".lore"), 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(Path(root), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(`{"schema_version":1,"pid":0}`); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(file.Fd()), syscall.LOCK_UN)

	_, err = Acquire(
		context.Background(),
		root,
		"preview",
		time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		0,
	)
	var contention *ContentionError
	if !errors.As(err, &contention) || contention.LegacyDirectory || contention.MetadataAvailable {
		t.Fatalf("contention = %#v, err = %v", contention, err)
	}
	if contention.Cause == nil {
		t.Fatal("malformed metadata cause is unavailable")
	}
}

func TestRejectsSymlinkAndNonRegularLockPaths(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{
			name: "symlink",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := os.Symlink(filepath.Join(t.TempDir(), "target"), path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			setup: func(t *testing.T, path string) {
				t.Helper()
				if err := syscall.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, ".lore"), 0o700); err != nil {
				t.Fatal(err)
			}
			test.setup(t, Path(root))
			_, err := Acquire(
				context.Background(),
				root,
				"capture",
				time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
				0,
			)
			if err == nil {
				t.Fatal("Acquire unexpectedly succeeded")
			}
		})
	}
}

func TestRejectsUnsafeCommandMetadata(t *testing.T) {
	_, err := Acquire(
		context.Background(),
		t.TempDir(),
		"capture\nsecret",
		time.Date(2026, 7, 22, 1, 2, 3, 0, time.UTC),
		0,
	)
	if err == nil {
		t.Fatal("Acquire unexpectedly accepted control characters")
	}
}

func writeLegacyMetadata(t *testing.T, lockDir string, startedAt time.Time) {
	t.Helper()
	metadata := struct {
		PID       int       `json:"pid"`
		Hostname  string    `json:"hostname"`
		Command   string    `json:"command"`
		StartedAt time.Time `json:"started_at"`
	}{
		PID:       os.Getpid(),
		Hostname:  "test-host",
		Command:   "legacy-writer",
		StartedAt: startedAt,
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lockDir, "metadata.json"), append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect helper marker: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for helper marker %s", path)
}
