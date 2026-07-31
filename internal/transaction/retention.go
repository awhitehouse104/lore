package transaction

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"time"
)

type RetentionPhase string

const (
	RetentionPruning RetentionPhase = "pruning"
	RetentionPruned  RetentionPhase = "pruned"
)

type RetentionArtifact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type RetentionReceipt struct {
	SchemaVersion int                 `json:"schema_version"`
	TransactionID string              `json:"transaction_id"`
	PreviewDigest string              `json:"preview_digest"`
	Phase         RetentionPhase      `json:"phase"`
	StartedAt     string              `json:"started_at"`
	CompletedAt   string              `json:"completed_at,omitempty"`
	Artifacts     []RetentionArtifact `json:"artifacts"`
}

func MarshalRetention(receipt RetentionReceipt) ([]byte, error) {
	if err := ValidateRetention(receipt); err != nil {
		return nil, err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return nil, fmt.Errorf("marshal retention receipt: %w", err)
	}
	return append(data, '\n'), nil
}

func DecodeRetention(data []byte) (RetentionReceipt, error) {
	var receipt RetentionReceipt
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return receipt, fmt.Errorf("decode retention receipt: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return receipt, fmt.Errorf("retention receipt contains multiple JSON values")
		}
		return receipt, fmt.Errorf("decode retention receipt: %w", err)
	}
	if err := ValidateRetention(receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}

func ValidateRetention(receipt RetentionReceipt) error {
	if receipt.SchemaVersion != SchemaVersion {
		return fmt.Errorf("retention schema_version must equal %d", SchemaVersion)
	}
	if err := ValidateTransactionID(receipt.TransactionID); err != nil {
		return err
	}
	if err := ValidateRevision(receipt.PreviewDigest); err != nil {
		return fmt.Errorf("retention preview_digest is invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, receipt.StartedAt); err != nil {
		return fmt.Errorf("retention started_at must be RFC 3339")
	}
	switch receipt.Phase {
	case RetentionPruning:
		if receipt.CompletedAt != "" {
			return fmt.Errorf("pruning retention receipt must not have completed_at")
		}
	case RetentionPruned:
		completedAt, err := time.Parse(time.RFC3339Nano, receipt.CompletedAt)
		if err != nil {
			return fmt.Errorf("pruned retention receipt requires an RFC 3339 completed_at")
		}
		startedAt, _ := time.Parse(time.RFC3339Nano, receipt.StartedAt)
		if completedAt.Before(startedAt) {
			return fmt.Errorf("retention completed_at precedes started_at")
		}
	default:
		return fmt.Errorf("invalid retention phase %q", receipt.Phase)
	}
	if len(receipt.Artifacts) < 3 || len(receipt.Artifacts) > MaxOperations+2 {
		return fmt.Errorf("retention artifacts must contain between 3 and %d entries", MaxOperations+2)
	}
	for index, artifact := range receipt.Artifacts {
		if index > 0 && receipt.Artifacts[index-1].Path >= artifact.Path {
			return fmt.Errorf("retention artifact paths must be unique and sorted")
		}
		if !validRetentionArtifactPath(artifact.Path) {
			return fmt.Errorf("invalid retention artifact path %q", artifact.Path)
		}
		if err := ValidateRevision(artifact.SHA256); err != nil {
			return fmt.Errorf("retention artifact %q has an invalid SHA-256", artifact.Path)
		}
		if artifact.Bytes < 0 || artifact.Bytes > maxRetentionArtifactBytes(artifact.Path) {
			return fmt.Errorf("retention artifact %q has an invalid byte count", artifact.Path)
		}
	}
	return nil
}

func ValidateRetentionForProposal(receipt RetentionReceipt, proposal Proposal, previewDigest string) error {
	if err := ValidateRetention(receipt); err != nil {
		return err
	}
	if receipt.TransactionID != proposal.TransactionID ||
		!DigestEqual(receipt.PreviewDigest, previewDigest) {
		return fmt.Errorf("retention receipt identity does not match transaction")
	}
	expected := make(map[string]string, len(proposal.Operations)+2)
	expected["diff.patch"] = proposal.DiffSHA256
	expected["lint.json"] = proposal.LintSHA256
	for _, operation := range proposal.Operations {
		expected[operation.ContentFile] = operation.ResultingContentSHA256
	}
	if len(receipt.Artifacts) != len(expected) {
		return fmt.Errorf("retention artifact count does not match proposal")
	}
	for _, artifact := range receipt.Artifacts {
		digest, exists := expected[artifact.Path]
		if !exists || !DigestEqual(artifact.SHA256, digest) {
			return fmt.Errorf("retention artifact %q does not match proposal", artifact.Path)
		}
	}
	return nil
}

func RetentionTotals(receipt RetentionReceipt) (files int, bytes int64) {
	for _, artifact := range receipt.Artifacts {
		files++
		bytes += artifact.Bytes
	}
	return files, bytes
}

func validRetentionArtifactPath(value string) bool {
	if value == "diff.patch" || value == "lint.json" {
		return true
	}
	dir, file := path.Split(value)
	if dir != "content/" || len(file) != len("000.md") || file[3:] != ".md" {
		return false
	}
	for _, character := range file[:3] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func maxRetentionArtifactBytes(value string) int64 {
	if value == "diff.patch" || value == "lint.json" {
		return MaxDiffBytes + 1
	}
	return MaxTotalNewContent
}
