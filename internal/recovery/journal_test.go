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

type journalFixture struct {
	root     string
	store    *recovery.Store
	journal  recovery.Journal
	original []byte
}

func newJournalFixture(t *testing.T) journalFixture {
	t.Helper()
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
	return journalFixture{
		root:     root,
		store:    store,
		journal:  journal,
		original: original,
	}
}

func TestJournalRoundTripAndPhaseValidation(t *testing.T) {
	fixture := newJournalFixture(t)
	first, err := recovery.Marshal(fixture.journal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := recovery.Marshal(fixture.journal)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || first[len(first)-1] != '\n' {
		t.Fatal("journal serialization is not deterministic")
	}
	if err := fixture.store.Create(fixture.journal, [][]byte{fixture.original}); err != nil {
		t.Fatal(err)
	}
	loaded, originals, err := fixture.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TransactionID != recoveryTransactionID || string(originals[0]) != string(fixture.original) {
		t.Fatalf("loaded journal = %+v originals=%q", loaded, originals)
	}
	loaded.Phase = recovery.PhaseFilesApplied
	if err := fixture.store.Update(loaded); err == nil {
		t.Fatal("invalid phase transition was accepted")
	}
	loaded.Phase = recovery.PhaseApplyingFiles
	if err := fixture.store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Phase = recovery.PhaseFilesApplied
	if err := fixture.store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Phase = recovery.PhaseGitCommitted
	loaded.Commit = "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	if err := fixture.store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	loaded.Phase = recovery.PhaseFinalized
	if err := fixture.store.Update(loaded); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixture.store.Load(); !os.IsNotExist(err) {
		t.Fatalf("removed journal still loads: %v", err)
	}
}

func TestJournalLoadRejectsCorruptionAndSymlinks(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*testing.T, journalFixture)
	}{
		{
			name: "active symlink",
			tamper: func(t *testing.T, fixture journalFixture) {
				t.Helper()
				active := filepath.Join(fixture.root, ".lore", "recovery", "active")
				outside := filepath.Join(t.TempDir(), "outside-active")
				if err := os.Mkdir(outside, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(active); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, active); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "journal symlink",
			tamper: func(t *testing.T, fixture journalFixture) {
				t.Helper()
				path := filepath.Join(fixture.root, ".lore", "recovery", "active", "journal.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside.json"), path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "original revision",
			tamper: func(t *testing.T, fixture journalFixture) {
				t.Helper()
				path := filepath.Join(fixture.root, ".lore", "recovery", "active", "originals", "000.md")
				if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "unknown journal field",
			tamper: func(t *testing.T, fixture journalFixture) {
				t.Helper()
				path := filepath.Join(fixture.root, ".lore", "recovery", "active", "journal.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				data = append([]byte(`{"unexpected_recovery_field":true,`), data[1:]...)
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "multiple JSON values",
			tamper: func(t *testing.T, fixture journalFixture) {
				t.Helper()
				path := filepath.Join(fixture.root, ".lore", "recovery", "active", "journal.json")
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("{}\n"); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newJournalFixture(t)
			if err := fixture.store.Create(fixture.journal, [][]byte{fixture.original}); err != nil {
				t.Fatal(err)
			}
			test.tamper(t, fixture)
			if _, _, err := fixture.store.Load(); err == nil {
				t.Fatal("tampered recovery journal unexpectedly loaded")
			}
		})
	}
}

func TestJournalCreateFailureLeavesNoPublishedOrTemporaryState(t *testing.T) {
	fixture := newJournalFixture(t)
	if err := fixture.store.Create(fixture.journal, [][]byte{[]byte("wrong original")}); err == nil {
		t.Fatal("mismatched recovery original unexpectedly succeeded")
	}

	recoveryRoot := filepath.Join(fixture.root, ".lore", "recovery")
	entries, err := os.ReadDir(recoveryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("failed recovery creation left artifacts: %v", entries)
	}
	if _, _, err := fixture.store.Load(); !os.IsNotExist(err) {
		t.Fatalf("failed recovery creation published active state: %v", err)
	}
}
