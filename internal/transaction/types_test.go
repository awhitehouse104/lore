package transaction

import (
	"strings"
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

func TestRetentionReceiptSerializationAndProposalBinding(t *testing.T) {
	proposal := testProposal()
	proposalBytes, err := MarshalProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	receipt := RetentionReceipt{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		PreviewDigest: Digest(proposalBytes),
		Phase:         RetentionPruned,
		StartedAt:     "2026-07-30T20:00:00Z",
		CompletedAt:   "2026-07-30T20:00:01Z",
		Artifacts: []RetentionArtifact{
			{Path: "content/000.md", SHA256: proposal.Operations[0].ResultingContentSHA256, Bytes: 7},
			{Path: "diff.patch", SHA256: proposal.DiffSHA256, Bytes: 4},
			{Path: "lint.json", SHA256: proposal.LintSHA256, Bytes: 4},
		},
	}
	first, err := MarshalRetention(receipt)
	if err != nil {
		t.Fatal(err)
	}
	second, err := MarshalRetention(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) || first[len(first)-1] != '\n' {
		t.Fatalf("retention serialization is not deterministic:\n%s", first)
	}
	decoded, err := DecodeRetention(first)
	if err != nil {
		t.Fatal(err)
	}
	malformed := append(append([]byte(nil), first...), '{')
	if _, err := DecodeRetention(malformed); err == nil || !strings.HasPrefix(err.Error(), "decode retention receipt: ") {
		t.Fatalf("malformed trailing JSON error = %v", err)
	}
	if err := ValidateRetentionForProposal(decoded, proposal, Digest(proposalBytes)); err != nil {
		t.Fatal(err)
	}
	files, bytes := RetentionTotals(decoded)
	if files != 3 || bytes != 15 {
		t.Fatalf("retention totals = %d files, %d bytes", files, bytes)
	}

	decoded.Artifacts[0].SHA256 = Digest([]byte("other"))
	if err := ValidateRetentionForProposal(decoded, proposal, Digest(proposalBytes)); err == nil {
		t.Fatal("retention receipt with mismatched artifact digest was accepted")
	}
}

func TestRetentionReceiptPhaseValidation(t *testing.T) {
	proposal := testProposal()
	proposalBytes, err := MarshalProposal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	base := RetentionReceipt{
		SchemaVersion: SchemaVersion,
		TransactionID: transactionID,
		PreviewDigest: Digest(proposalBytes),
		Phase:         RetentionPruning,
		StartedAt:     "2026-07-30T20:00:00Z",
		Artifacts: []RetentionArtifact{
			{Path: "content/000.md", SHA256: proposal.Operations[0].ResultingContentSHA256, Bytes: 7},
			{Path: "diff.patch", SHA256: proposal.DiffSHA256, Bytes: 4},
			{Path: "lint.json", SHA256: proposal.LintSHA256, Bytes: 4},
		},
	}
	if err := ValidateRetention(base); err != nil {
		t.Fatal(err)
	}
	withCompletion := base
	withCompletion.CompletedAt = "2026-07-30T20:00:01Z"
	if err := ValidateRetention(withCompletion); err == nil {
		t.Fatal("pruning receipt with completed_at was accepted")
	}
	pruned := base
	pruned.Phase = RetentionPruned
	if err := ValidateRetention(pruned); err == nil {
		t.Fatal("pruned receipt without completed_at was accepted")
	}
}

func TestRetentionArtifactPathsRequireThreeASCIIDigits(t *testing.T) {
	for _, value := range []string{"content/000.md", "content/999.md", "diff.patch", "lint.json"} {
		if !validRetentionArtifactPath(value) {
			t.Errorf("valid retention artifact path %q was rejected", value)
		}
	}
	for _, value := range []string{
		"content/+12.md",
		"content/-12.md",
		"content/12a.md",
		"content/12.md",
		"content/000.txt",
		"other/000.md",
	} {
		if validRetentionArtifactPath(value) {
			t.Errorf("invalid retention artifact path %q was accepted", value)
		}
	}
}
