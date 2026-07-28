package transaction

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/repository"
)

func TestStoreRoundTripPermissionsAndTamperDetection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	proposal := testProposal()
	state := State{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		Status:        StatusPreviewed,
		UpdatedAt:     proposal.CreatedAt,
	}
	digest, err := store.Save(Artifacts{
		Proposal: proposal,
		State:    state,
		Diff:     []byte("diff"),
		Lint:     []byte("lint"),
		Contents: [][]byte{[]byte("content")},
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := store.Load(transactionID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.PreviewDigest != digest || string(loaded.Contents[0]) != "content" {
		t.Fatalf("loaded artifacts = %+v", loaded)
	}
	dir := filepath.Join(root, ".lore", "transactions", transactionID)
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("transaction mode = %o", info.Mode().Perm())
	}
	info, err = os.Stat(filepath.Join(dir, "proposal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("proposal mode = %o", info.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, "diff.patch"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(transactionID); err == nil {
		t.Fatal("tampered diff was accepted")
	}
}

func TestStoreDiscardIsIdempotentAndRetainsReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	if _, err := initrepo.Initialize(context.Background(), initrepo.Options{Path: root, NoGit: true}, gitx.New()); err != nil {
		t.Fatal(err)
	}
	repo, err := repository.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	proposal := testProposal()
	_, err = store.Save(Artifacts{
		Proposal: proposal,
		State: State{
			SchemaVersion: SchemaVersion,
			TransactionID: transactionID,
			Status:        StatusPreviewed,
			UpdatedAt:     proposal.CreatedAt,
		},
		Diff:     []byte("diff"),
		Lint:     []byte("lint"),
		Contents: [][]byte{[]byte("content")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Discard(transactionID, "2026-07-28T21:00:00Z"); err != nil {
		t.Fatal(err)
	}
	state, err := store.Discard(transactionID, "2026-07-28T22:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != StatusDiscarded {
		t.Fatalf("state = %+v", state)
	}
	loaded, err := store.Load(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Status != StatusDiscarded || len(loaded.Diff) != 0 || len(loaded.Contents) != 0 {
		t.Fatalf("discarded artifacts = %+v", loaded)
	}
	if _, err := os.Stat(filepath.Join(root, ".lore", "transactions", transactionID, "proposal.json")); err != nil {
		t.Fatalf("receipt proposal missing: %v", err)
	}
}
