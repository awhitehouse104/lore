package transaction

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"lore/internal/config"
	"lore/internal/repository"
)

func TestStoreRoundTripPermissionsAndTamperDetection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	repo := testRepository(t, root)
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
	repo := testRepository(t, root)
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
	if _, err := os.Stat(filepath.Join(root, ".lore", "transactions", transactionID, "lint.json")); !os.IsNotExist(err) {
		t.Fatalf("discarded lint artifact remains: %v", err)
	}
}

func TestStorePruneCompactsCommittedTransactionAndRetainsReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	repo := testRepository(t, root)
	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	saveCommittedTransaction(t, store)

	inspection, err := store.InspectPrune(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := int64(len("diff") + len("lint") + len("content"))
	if inspection.PayloadFiles != 3 ||
		inspection.PayloadBytes != wantBytes ||
		inspection.RemainingPayloadFiles != 3 {
		t.Fatalf("inspection = %+v", inspection)
	}
	result, err := store.Prune(context.Background(), transactionID, "2026-07-30T20:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.AlreadyPruned || result.FilesRemoved != 3 || result.BytesRemoved != wantBytes ||
		result.Retention.Phase != RetentionPruned {
		t.Fatalf("prune result = %+v", result)
	}
	dir := filepath.Join(root, ".lore", "transactions", transactionID)
	for _, relative := range []string{"diff.patch", "lint.json", "content"} {
		if _, err := os.Lstat(filepath.Join(dir, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("pruned artifact %s remains: %v", relative, err)
		}
	}
	for _, relative := range []string{"proposal.json", "state.json", "retention.json"} {
		info, err := os.Lstat(filepath.Join(dir, relative))
		if err != nil {
			t.Fatalf("receipt artifact %s missing: %v", relative, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("receipt artifact %s mode = %o", relative, info.Mode().Perm())
		}
	}
	loaded, err := store.Load(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Status != StatusCommitted || loaded.Retention == nil ||
		loaded.Retention.Phase != RetentionPruned || len(loaded.Contents) != 0 ||
		len(loaded.Diff) != 0 || len(loaded.Lint) != 0 {
		t.Fatalf("loaded pruned receipt = %+v", loaded)
	}
	again, err := store.Prune(context.Background(), transactionID, "2026-07-31T20:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyPruned || again.FilesRemoved != 0 || again.BytesRemoved != 0 {
		t.Fatalf("idempotent prune = %+v", again)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "diff.patch")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectPrune(transactionID); err == nil {
		t.Fatal("finalized prune accepted a reintroduced symlink payload")
	}
	data, err := os.ReadFile(outside)
	if err != nil || string(data) != "outside" {
		t.Fatalf("outside payload changed: %q, %v", data, err)
	}
}

func TestStorePruneResumesAfterEveryDurableMarker(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	repo := testRepository(t, root)
	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	saveCommittedTransaction(t, store)
	injected := errors.New("injected interruption")
	store.afterPruneStep = func(step string) error {
		if step == "content/000.md" {
			return injected
		}
		return nil
	}
	_, err = store.Prune(context.Background(), transactionID, "2026-07-30T20:00:00Z")
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted prune error = %v", err)
	}
	receipt, err := store.LoadReceipt(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Retention == nil || receipt.Retention.Phase != RetentionPruning {
		t.Fatalf("interrupted retention = %+v", receipt.Retention)
	}
	inspection, err := store.InspectPrune(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.RemainingPayloadFiles != 2 {
		t.Fatalf("remaining inspection = %+v", inspection)
	}

	resumed, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	result, err := resumed.Prune(context.Background(), transactionID, "2026-07-31T20:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if result.Retention.Phase != RetentionPruned || result.FilesRemoved != 2 {
		t.Fatalf("resumed prune = %+v", result)
	}
}

func TestStorePruneCancellationLeavesResumableReceipt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	repo := testRepository(t, root)
	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	saveCommittedTransaction(t, store)
	ctx, cancel := context.WithCancel(context.Background())
	store.afterPruneStep = func(step string) error {
		if step == "retention.json:pruning" {
			cancel()
		}
		return nil
	}
	_, err = store.Prune(ctx, transactionID, "2026-07-30T20:00:00Z")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prune error = %v", err)
	}
	receipt, err := store.LoadReceipt(transactionID)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Retention == nil || receipt.Retention.Phase != RetentionPruning {
		t.Fatalf("canceled retention = %+v", receipt.Retention)
	}
	if _, err := NewStore(repo); err != nil {
		t.Fatal(err)
	}
}

func TestStorePruneRejectsUnexpectedAndSymlinkArtifacts(t *testing.T) {
	t.Run("unexpected file", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "knowledge")
		repo := testRepository(t, root)
		store, err := NewStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		saveCommittedTransaction(t, store)
		dir := filepath.Join(root, ".lore", "transactions", transactionID)
		if err := os.WriteFile(filepath.Join(dir, "unexpected"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Prune(context.Background(), transactionID, "2026-07-30T20:00:00Z"); err == nil {
			t.Fatal("prune accepted an unexpected artifact")
		}
		if _, err := os.Lstat(filepath.Join(dir, "retention.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prune mutated before layout validation: %v", err)
		}
	})

	t.Run("content symlink", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "knowledge")
		repo := testRepository(t, root)
		store, err := NewStore(repo)
		if err != nil {
			t.Fatal(err)
		}
		saveCommittedTransaction(t, store)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
		content := filepath.Join(root, ".lore", "transactions", transactionID, "content", "000.md")
		if err := os.Remove(content); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, content); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Prune(context.Background(), transactionID, "2026-07-30T20:00:00Z"); err == nil {
			t.Fatal("prune accepted a symlink artifact")
		}
		data, err := os.ReadFile(outside)
		if err != nil || string(data) != "outside" {
			t.Fatalf("outside file changed: %q, %v", data, err)
		}
	})
}

func TestStorePruneRejectsEveryNonCommittedState(t *testing.T) {
	for _, status := range []Status{
		StatusPreviewed,
		StatusApplying,
		StatusFailed,
		StatusRecoveryRequired,
		StatusDiscarded,
	} {
		t.Run(string(status), func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "knowledge")
			repo := testRepository(t, root)
			store, err := NewStore(repo)
			if err != nil {
				t.Fatal(err)
			}
			proposal := testProposal()
			digest, err := store.Save(Artifacts{
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
			if status != StatusPreviewed {
				state := State{
					SchemaVersion: SchemaVersion,
					TransactionID: transactionID,
					Status:        status,
					UpdatedAt:     "2026-07-28T20:20:00Z",
					PreviewDigest: digest,
				}
				if err := store.UpdateState(transactionID, state); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Prune(context.Background(), transactionID, "2026-07-30T20:00:00Z"); err == nil {
				t.Fatalf("prune accepted state %s", status)
			}
			if _, err := os.Lstat(filepath.Join(root, ".lore", "transactions", transactionID, "retention.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("state %s wrote a retention receipt: %v", status, err)
			}
		})
	}
}

func TestStoreListIDsStrictRejectsTransactionSymlink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge")
	repo := testRepository(t, root)
	store, err := NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".lore", "transactions"), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, ".lore", "transactions", transactionID)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListIDsStrict(); err == nil {
		t.Fatal("strict listing accepted a transaction symlink")
	}
}

func saveCommittedTransaction(t *testing.T, store *Store) {
	t.Helper()
	proposal := testProposal()
	digest, err := store.Save(Artifacts{
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
	state := State{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		Status:        StatusApplying,
		UpdatedAt:     "2026-07-28T20:20:00Z",
		PreviewDigest: digest,
	}
	if err := store.UpdateState(transactionID, state); err != nil {
		t.Fatal(err)
	}
	state.Status = StatusCommitted
	state.UpdatedAt = "2026-07-28T20:30:00Z"
	state.CommittedAt = "2026-07-28T20:30:00Z"
	state.Commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if err := store.UpdateState(transactionID, state); err != nil {
		t.Fatal(err)
	}
}

func testRepository(t *testing.T, root string) *repository.Repository {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".lore"), 0o700); err != nil {
		t.Fatal(err)
	}
	return &repository.Repository{Root: root, Config: config.Defaults()}
}
