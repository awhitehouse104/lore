package transaction

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"lore/internal/docs"

	"github.com/oklog/ulid/v2"
)

const DefaultActor = "local-cli"

type IDGenerator interface {
	New(time.Time) (string, error)
}

type CryptoIDGenerator struct{}

func (CryptoIDGenerator) New(now time.Time) (string, error) {
	return NewID(now)
}

type Proposal struct {
	SchemaVersion int                  `json:"schema_version"`
	TransactionID string               `json:"transaction_id"`
	CreatedAt     string               `json:"created_at"`
	BaseCommit    string               `json:"base_commit"`
	BaseBranch    string               `json:"base_branch"`
	Actor         string               `json:"actor"`
	Message       string               `json:"message"`
	Operations    []EffectiveOperation `json:"operations"`
	ChangedPaths  []string             `json:"changed_paths"`
	DiffSHA256    string               `json:"diff_sha256"`
	LintSHA256    string               `json:"lint_sha256"`
}

type EffectiveOperation struct {
	Op                     OperationKind `json:"op"`
	Path                   string        `json:"path"`
	ExpectedRevision       string        `json:"expected_revision,omitempty"`
	PageIDs                []string      `json:"page_ids,omitempty"`
	Sensitivity            string        `json:"sensitivity,omitempty"`
	AllowDowngrade         bool          `json:"allow_downgrade,omitempty"`
	OriginalRevision       string        `json:"original_revision,omitempty"`
	Deleted                bool          `json:"deleted,omitempty"`
	ResultingContentSHA256 string        `json:"resulting_content_sha256,omitempty"`
	ContentFile            string        `json:"content_file,omitempty"`
}

type Status string

const (
	StatusPreviewed        Status = "previewed"
	StatusApplying         Status = "applying"
	StatusCommitted        Status = "committed"
	StatusDiscarded        Status = "discarded"
	StatusFailed           Status = "failed"
	StatusRecoveryRequired Status = "recovery_required"
)

type State struct {
	SchemaVersion  int         `json:"schema_version"`
	TransactionID  string      `json:"transaction_id"`
	Status         Status      `json:"status"`
	UpdatedAt      string      `json:"updated_at"`
	PreviewDigest  string      `json:"preview_digest"`
	Commit         string      `json:"commit,omitempty"`
	CommittedAt    string      `json:"committed_at,omitempty"`
	FailureCode    string      `json:"failure_code,omitempty"`
	FailureMessage string      `json:"failure_message,omitempty"`
	Pushed         bool        `json:"pushed,omitempty"`
	PushError      string      `json:"push_error,omitempty"`
	Lint           LintSummary `json:"lint"`
}

type LintSummary struct {
	Valid    bool `json:"valid"`
	Errors   int  `json:"errors"`
	Warnings int  `json:"warnings"`
}

func NewID(now time.Time) (string, error) {
	value, err := ulid.New(ulid.Timestamp(now.UTC()), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate transaction ULID: %w", err)
	}
	return "tx_" + value.String(), nil
}

func MarshalProposal(proposal Proposal) ([]byte, error) {
	if err := ValidateProposal(proposal); err != nil {
		return nil, err
	}
	data, err := json.Marshal(proposal)
	if err != nil {
		return nil, fmt.Errorf("marshal proposal: %w", err)
	}
	return append(data, '\n'), nil
}

func MarshalState(state State) ([]byte, error) {
	if err := ValidateState(state); err != nil {
		return nil, err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, fmt.Errorf("marshal transaction state: %w", err)
	}
	return append(data, '\n'), nil
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DigestEqual(left, right string) bool {
	leftBytes := []byte(left)
	rightBytes := []byte(right)
	return len(leftBytes) == len(rightBytes) && subtle.ConstantTimeCompare(leftBytes, rightBytes) == 1
}

func ValidateProposal(proposal Proposal) error {
	if proposal.SchemaVersion != SchemaVersion {
		return fmt.Errorf("proposal schema_version must equal %d", SchemaVersion)
	}
	if err := ValidateTransactionID(proposal.TransactionID); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, proposal.CreatedAt); err != nil {
		return fmt.Errorf("proposal created_at must be RFC 3339")
	}
	if err := ValidateGitHash(proposal.BaseCommit); err != nil {
		return fmt.Errorf("proposal base commit is invalid")
	}
	if proposal.BaseBranch == "" {
		return fmt.Errorf("proposal base branch is required")
	}
	if proposal.Actor == "" {
		return fmt.Errorf("proposal actor is required")
	}
	if err := ValidateMessage(proposal.Message); err != nil {
		return err
	}
	if len(proposal.Operations) == 0 || len(proposal.Operations) > MaxOperations {
		return fmt.Errorf("proposal must contain between 1 and %d operations", MaxOperations)
	}
	if len(proposal.ChangedPaths) != len(proposal.Operations) {
		return fmt.Errorf("proposal changed_paths must correspond to operations")
	}
	if err := ValidateRevision(proposal.DiffSHA256); err != nil {
		return fmt.Errorf("invalid diff_sha256")
	}
	if err := ValidateRevision(proposal.LintSHA256); err != nil {
		return fmt.Errorf("invalid lint_sha256")
	}
	seen := make(map[string]struct{}, len(proposal.Operations))
	for index, operation := range proposal.Operations {
		if index > 0 && proposal.ChangedPaths[index-1] >= proposal.ChangedPaths[index] {
			return fmt.Errorf("proposal changed_paths must be unique and sorted")
		}
		if _, exists := seen[operation.Path]; exists {
			return fmt.Errorf("proposal contains duplicate path %q", operation.Path)
		}
		seen[operation.Path] = struct{}{}
		if proposal.ChangedPaths[index] != operation.Path {
			return fmt.Errorf("proposal changed_paths are not in operation order")
		}
		if operation.Deleted {
			if operation.ResultingContentSHA256 != "" || operation.ContentFile != "" {
				return fmt.Errorf("operation %d deleted result must not have content metadata", index)
			}
		} else {
			if err := ValidateRevision(operation.ResultingContentSHA256); err != nil {
				return fmt.Errorf("operation %d has invalid resulting content hash", index)
			}
			if operation.ContentFile != fmt.Sprintf("content/%03d.md", index) {
				return fmt.Errorf("operation %d has invalid content file", index)
			}
		}
		switch operation.Op {
		case OperationCreatePage:
			if err := ValidatePagePath(operation.Path); err != nil {
				return err
			}
			if operation.OriginalRevision != "" || operation.ExpectedRevision != "" {
				return fmt.Errorf("create_page operation must not have an original revision")
			}
			if operation.Deleted {
				return fmt.Errorf("create_page operation must have resulting content")
			}
		case OperationUpdatePage, OperationPatchPage:
			if err := ValidatePagePath(operation.Path); err != nil {
				return err
			}
			if err := ValidateRevision(operation.ExpectedRevision); err != nil {
				return err
			}
			if operation.OriginalRevision != operation.ExpectedRevision {
				return fmt.Errorf("%s original revision must equal expected revision", operation.Op)
			}
			if operation.Deleted {
				return fmt.Errorf("%s operation must have resulting content", operation.Op)
			}
		case OperationDeletePage:
			if err := ValidatePagePath(operation.Path); err != nil {
				return err
			}
			if err := ValidateRevision(operation.ExpectedRevision); err != nil {
				return err
			}
			if operation.OriginalRevision != operation.ExpectedRevision {
				return fmt.Errorf("delete_page original revision must equal expected revision")
			}
			if !operation.Deleted {
				return fmt.Errorf("delete_page operation must have an absent result")
			}
			if !docs.ValidSensitivity(operation.Sensitivity) {
				return fmt.Errorf("delete_page operation must record target sensitivity")
			}
		case OperationMarkSourceIntegrated:
			if err := ValidateSourcePath(operation.Path); err != nil {
				return err
			}
			if err := ValidateRevision(operation.ExpectedRevision); err != nil {
				return err
			}
			if operation.OriginalRevision != operation.ExpectedRevision {
				return fmt.Errorf("mark_source_integrated original revision must equal expected revision")
			}
			if operation.Deleted {
				return fmt.Errorf("mark_source_integrated operation must have resulting content")
			}
		case OperationSetSourceSensitivity:
			if err := ValidateSourcePath(operation.Path); err != nil {
				return err
			}
			if err := ValidateRevision(operation.ExpectedRevision); err != nil {
				return err
			}
			if operation.OriginalRevision != operation.ExpectedRevision {
				return fmt.Errorf("set_source_sensitivity original revision must equal expected revision")
			}
			if !docs.ValidSensitivity(operation.Sensitivity) {
				return fmt.Errorf("set_source_sensitivity has invalid sensitivity")
			}
			if operation.Deleted {
				return fmt.Errorf("set_source_sensitivity operation must have resulting content")
			}
		default:
			return fmt.Errorf("operation %d has invalid kind %q", index, operation.Op)
		}
	}
	return nil
}

func ValidateState(state State) error {
	if state.SchemaVersion != SchemaVersion {
		return fmt.Errorf("state schema_version must equal %d", SchemaVersion)
	}
	if err := ValidateTransactionID(state.TransactionID); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, state.UpdatedAt); err != nil {
		return fmt.Errorf("state updated_at must be RFC 3339")
	}
	if err := ValidateRevision(state.PreviewDigest); err != nil {
		return fmt.Errorf("state preview_digest is invalid")
	}
	switch state.Status {
	case StatusPreviewed, StatusApplying, StatusCommitted, StatusDiscarded, StatusFailed, StatusRecoveryRequired:
	default:
		return fmt.Errorf("invalid transaction status %q", state.Status)
	}
	if state.Status == StatusCommitted && state.Commit == "" {
		return fmt.Errorf("committed transaction state requires a commit hash")
	}
	if state.Status == StatusCommitted {
		if err := ValidateGitHash(state.Commit); err != nil {
			return fmt.Errorf("committed transaction state has an invalid commit hash")
		}
		if _, err := time.Parse(time.RFC3339Nano, state.CommittedAt); err != nil {
			return fmt.Errorf("committed transaction state requires an RFC 3339 committed_at")
		}
	}
	return nil
}

func ValidateTransition(from, to Status) error {
	if from == to {
		return nil
	}
	allowed := map[Status]map[Status]bool{
		StatusPreviewed: {
			StatusApplying:         true,
			StatusDiscarded:        true,
			StatusFailed:           true,
			StatusRecoveryRequired: true,
		},
		StatusApplying: {
			StatusCommitted:        true,
			StatusFailed:           true,
			StatusRecoveryRequired: true,
		},
		StatusRecoveryRequired: {
			StatusCommitted: true,
			StatusFailed:    true,
		},
		StatusFailed: {
			StatusDiscarded:        true,
			StatusRecoveryRequired: true,
		},
	}
	if !allowed[from][to] {
		return fmt.Errorf("invalid transaction state transition from %s to %s", from, to)
	}
	return nil
}
