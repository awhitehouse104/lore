package transaction

import (
	"testing"
	"time"
)

const transactionID = "tx_01ARZ3NDEKTSV4RRFFQ69G5FAV"

func testProposal() Proposal {
	return Proposal{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		CreatedAt:     "2026-07-28T20:10:00Z",
		BaseCommit:    "0123456789012345678901234567890123456789",
		BaseBranch:    "main",
		Actor:         DefaultActor,
		Message:       "create: a page",
		Operations: []EffectiveOperation{{
			Op:                     OperationCreatePage,
			Path:                   "pages/new.md",
			ResultingContentSHA256: Digest([]byte("content")),
			ContentFile:            "content/000.md",
		}},
		ChangedPaths: []string{"pages/new.md"},
		DiffSHA256:   Digest([]byte("diff")),
		LintSHA256:   Digest([]byte("lint")),
	}
}

func TestProposalSerializationAndDigestAreDeterministic(t *testing.T) {
	first, err := MarshalProposal(testProposal())
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalProposal(testProposal())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("proposal bytes differ:\n%s\n%s", first, second)
	}
	if first[len(first)-1] != '\n' {
		t.Fatal("proposal lacks trailing newline")
	}
	if Digest(first) != Digest(second) {
		t.Fatal("proposal digests differ")
	}
	if !DigestEqual(Digest(first), Digest(second)) {
		t.Fatal("equal digests did not compare equal")
	}
	if DigestEqual(Digest(first), Digest([]byte("other"))) {
		t.Fatal("different digests compared equal")
	}
}

func TestTransactionStateTransitions(t *testing.T) {
	valid := [][2]Status{
		{StatusPreviewed, StatusApplying},
		{StatusPreviewed, StatusDiscarded},
		{StatusApplying, StatusCommitted},
		{StatusApplying, StatusRecoveryRequired},
		{StatusRecoveryRequired, StatusFailed},
		{StatusRecoveryRequired, StatusCommitted},
		{StatusFailed, StatusDiscarded},
	}
	for _, transition := range valid {
		if err := ValidateTransition(transition[0], transition[1]); err != nil {
			t.Errorf("%s -> %s: %v", transition[0], transition[1], err)
		}
	}
	invalid := [][2]Status{
		{StatusPreviewed, StatusCommitted},
		{StatusCommitted, StatusApplying},
		{StatusDiscarded, StatusPreviewed},
		{StatusFailed, StatusApplying},
	}
	for _, transition := range invalid {
		if err := ValidateTransition(transition[0], transition[1]); err == nil {
			t.Errorf("%s -> %s accepted", transition[0], transition[1])
		}
	}
}

func TestNewIDProducesTransactionID(t *testing.T) {
	value, err := NewID(time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTransactionID(value); err != nil {
		t.Fatalf("NewID = %q: %v", value, err)
	}
}
