package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"
)

type fixedClock struct {
	now time.Time
}

func (c *fixedClock) Now() time.Time { return c.now }

func TestStoreReplayConflictAndNoInputBody(t *testing.T) {
	repo := testRepository(t)
	clock := &fixedClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	store, err := NewStore(repo, clock, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	input := struct {
		Text string `json:"text"`
	}{Text: "seeded secret capture body"}
	digest, err := DigestInput(input)
	if err != nil {
		t.Fatal(err)
	}
	lease, replay, found, err := store.Begin("principal_one", "lore_capture", "retry-key", digest)
	if err != nil || found || replay != nil {
		t.Fatalf("Begin = lease=%v replay=%q found=%v err=%v", lease, replay, found, err)
	}
	result := struct {
		ID string `json:"id"`
	}{ID: "src_01ARZ3NDEKTSV4RRFFQ69G5FAV"}
	if err := lease.Commit(result); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	_, replay, found, err = store.Begin("principal_one", "lore_capture", "retry-key", digest)
	if err != nil || !found {
		t.Fatalf("replay Begin = found=%v err=%v", found, err)
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(replay, &decoded); err != nil || decoded.ID != result.ID {
		t.Fatalf("replay = %q, decoded=%+v, err=%v", replay, decoded, err)
	}
	differentDigest, _ := DigestInput(struct {
		Text string `json:"text"`
	}{Text: "different"})
	_, _, _, err = store.Begin("principal_one", "lore_capture", "retry-key", differentDigest)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("different-input error = %T %v", err, err)
	}
	files, err := os.ReadDir(filepath.Join(repo.Root, ".lore", "idempotency"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(repo.Root, ".lore", "idempotency", file.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), input.Text) || strings.Contains(string(data), "retry-key") {
			t.Fatalf("record leaked input or key: %s", data)
		}
	}
}

func TestStoreExpiryAndConcurrentLease(t *testing.T) {
	repo := testRepository(t)
	clock := &fixedClock{now: time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)}
	store, err := NewStore(repo, clock, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := DigestInput(map[string]string{"value": "one"})
	lease, _, _, err := store.Begin("principal_one", "lore_capture", "key", digest)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = store.Begin("principal_one", "lore_capture", "key", digest)
	var locked *LockedError
	if !errors.As(err, &locked) {
		t.Fatalf("concurrent error = %T %v", err, err)
	}
	if err := lease.Commit(map[string]string{"id": "one"}); err != nil {
		t.Fatal(err)
	}
	_ = lease.Close()
	lockPath := filepath.Join(repo.Root, ".lore", "idempotency", keyHash("principal_one", "lore_capture", "key")+".lock")
	if info, err := os.Lstat(lockPath); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("closed lease lock should remain stable until serialized cleanup: info=%v err=%v", info, err)
	}
	clock.now = clock.now.Add(2 * time.Hour)
	next, _, found, err := store.Begin("principal_one", "lore_capture", "key", digest)
	if err != nil || found || next == nil {
		t.Fatalf("expired Begin = lease=%v found=%v err=%v", next, found, err)
	}
	_ = next.Close()
}

func TestStoreRejectsInvalidKeys(t *testing.T) {
	repo := testRepository(t)
	store, err := NewStore(repo, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := DigestInput(map[string]string{"value": "one"})
	for _, key := range []string{"", "line\nbreak", strings.Repeat("x", MaximumKeyBytes+1)} {
		if _, _, _, err := store.Begin("principal", "operation", key, digest); err == nil {
			t.Fatalf("invalid key %q succeeded", key)
		}
	}
}

func TestStoreRejectsSymlinkRecordsAndLeases(t *testing.T) {
	repo := testRepository(t)
	store, err := NewStore(repo, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := DigestInput(map[string]string{"value": "one"})
	seed, _, _, err := store.Begin("principal", "operation", "seed", digest)
	if err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(repo.Root, ".lore", "idempotency")

	recordHash := keyHash("principal", "operation", "record-link")
	if err := os.Symlink(outside, filepath.Join(root, recordHash+".json")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Begin("principal", "operation", "record-link", digest); err == nil {
		t.Fatal("symlink record unexpectedly succeeded")
	}

	leaseHash := keyHash("principal", "operation", "lease-link")
	if err := os.Symlink(outside, filepath.Join(root, leaseHash+".lock")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.Begin("principal", "operation", "lease-link", digest); err == nil {
		t.Fatal("symlink lease unexpectedly succeeded")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "untouched" {
		t.Fatalf("outside file changed: data=%q err=%v", data, err)
	}
}

func testRepository(t *testing.T) *repository.Repository {
	t.Helper()
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}
