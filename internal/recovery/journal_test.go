package recovery_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"lore/internal/config"
	"lore/internal/docs"
	"lore/internal/recovery"
	"lore/internal/repository"
	"lore/internal/transaction"
)

const recoveryTransactionID = "tx_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func TestJournalRoundTripAndPhaseValidation(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".lore"), 0o700); err != nil {
		t.Fatal(err)
	}
	repo := &repository.Repository{Root: root, Config: config.Defaults()}
	store, err := recovery.NewStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	original := []byte("original")
	proposal := transaction.Proposal{
		SchemaVersion: transaction.SchemaVersion,
		TransactionID: recoveryTransactionID,
		CreatedAt:     "2026-07-28T20:10:00Z",
		BaseCommit:    "0123456789012345678901234567890123456789",
		BaseBranch:    "main",
		Actor:         transaction.DefaultActor,
		Message:       "update: journal",
		Operations: []transaction.EffectiveOperation{{
			Op:                     transaction.OperationUpdatePage,
			Path:                   "pages/example.md",
			ExpectedRevision:       docs.Revision(original),
			OriginalRevision:       docs.Revision(original),
			ResultingContentSHA256: docs.Revision([]byte("result")),
			ContentFile:            "content/000.md",
		}},
		ChangedPaths: []string{"pages/example.md"},
		DiffSHA256:   docs.Revision([]byte("diff")),
		LintSHA256:   docs.Revision([]byte("lint")),
	}
	journal, err := recovery.NewJournal(
		proposal,
		docs.Revision([]byte("proposal")),
		[][]byte{original},
		[]bool{true},
		time.Date(2026, 7, 28, 20, 10, 0, 0, time.UTC),
		"commit",
	)
	if err != nil {
		t.Fatal(err)
	}
	first, err := recovery.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := recovery.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || first[len(first)-1] != '\n' {
		t.Fatal("journal serialization is not deterministic")
	}
	if err := store.Create(journal, [][]byte{original}); err != nil {
		t.Fatal(err)
	}
	loaded, originals, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TransactionID != recoveryTransactionID || string(originals[0]) != string(original) {
		t.Fatalf("loaded journal = %+v originals=%q", loaded, originals)
	}
	loaded.Phase = recovery.PhaseFilesApplied
	if err := store.Update(loaded); err == nil {
		t.Fatal("invalid phase transition was accepted")
	}
	loaded.Phase = recovery.PhaseApplyingFiles
	if err := store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Phase = recovery.PhaseFilesApplied
	if err := store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Phase = recovery.PhaseGitCommitted
	loaded.Commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if err := store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Phase = recovery.PhaseFinalized
	if err := store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Load(); !os.IsNotExist(err) {
		t.Fatalf("removed journal still loads: %v", err)
	}
}
