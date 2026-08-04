package transaction

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"lore/internal/repository"
)

const maxArtifactBytes = MaxDiffBytes + 1

type Artifacts struct {
	Proposal      Proposal
	State         State
	PreviewDigest string
	Retention     *RetentionReceipt
	Diff          []byte
	Lint          []byte
	Contents      [][]byte
}

type Receipt struct {
	Proposal      Proposal
	State         State
	PreviewDigest string
	Retention     *RetentionReceipt
}

type Store struct {
	repo           *repository.Repository
	root           string
	afterPruneStep func(string) error
}

type PruneInspection struct {
	Proposal              Proposal
	State                 State
	PreviewDigest         string
	Retention             *RetentionReceipt
	PayloadFiles          int
	PayloadBytes          int64
	RemainingPayloadFiles int
	RemainingPayloadBytes int64
}

type PruneResult struct {
	Retention     RetentionReceipt
	FilesRemoved  int
	BytesRemoved  int64
	AlreadyPruned bool
}

func NewStore(repo *repository.Repository) (*Store, error) {
	root, err := repo.SafeRepositoryPath(".lore/transactions")
	if err != nil {
		return nil, fmt.Errorf("resolve transaction store: %w", err)
	}
	return &Store{repo: repo, root: root}, nil
}

func (s *Store) Save(artifacts Artifacts) (string, error) {
	proposalBytes, err := MarshalProposal(artifacts.Proposal)
	if err != nil {
		return "", err
	}
	digest := Digest(proposalBytes)
	artifacts.State.PreviewDigest = digest
	if artifacts.State.TransactionID != artifacts.Proposal.TransactionID {
		return "", fmt.Errorf("proposal and state transaction IDs differ")
	}
	if artifacts.State.Status != StatusPreviewed {
		return "", fmt.Errorf("new transaction state must be previewed")
	}
	if Digest(artifacts.Diff) != artifacts.Proposal.DiffSHA256 {
		return "", fmt.Errorf("diff artifact hash does not match proposal")
	}
	if Digest(artifacts.Lint) != artifacts.Proposal.LintSHA256 {
		return "", fmt.Errorf("lint artifact hash does not match proposal")
	}
	if len(artifacts.Contents) != len(artifacts.Proposal.Operations) {
		return "", fmt.Errorf("content artifact count does not match proposal operations")
	}
	for index, content := range artifacts.Contents {
		operation := artifacts.Proposal.Operations[index]
		if operation.Deleted {
			if content != nil {
				return "", fmt.Errorf("deleted operation %d must not have a content artifact", index)
			}
			continue
		}
		if Digest(content) != operation.ResultingContentSHA256 {
			return "", fmt.Errorf("content artifact %d hash does not match proposal", index)
		}
	}
	stateBytes, err := MarshalState(artifacts.State)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", fmt.Errorf("create transaction store: %w", err)
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return "", fmt.Errorf("set transaction store permissions: %w", err)
	}
	temp, err := os.MkdirTemp(s.root, ".tmp-")
	if err != nil {
		return "", fmt.Errorf("create transaction temporary directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	if err := os.Chmod(temp, 0o700); err != nil {
		cleanup()
		return "", fmt.Errorf("set transaction directory permissions: %w", err)
	}
	contentDir := filepath.Join(temp, "content")
	if err := os.Mkdir(contentDir, 0o700); err != nil {
		cleanup()
		return "", fmt.Errorf("create transaction content directory: %w", err)
	}
	files := []struct {
		path string
		data []byte
	}{
		{filepath.Join(temp, "proposal.json"), proposalBytes},
		{filepath.Join(temp, "state.json"), stateBytes},
		{filepath.Join(temp, "diff.patch"), artifacts.Diff},
		{filepath.Join(temp, "lint.json"), artifacts.Lint},
	}
	for index, content := range artifacts.Contents {
		if artifacts.Proposal.Operations[index].Deleted {
			continue
		}
		files = append(files, struct {
			path string
			data []byte
		}{filepath.Join(contentDir, fmt.Sprintf("%03d.md", index)), content})
	}
	for _, file := range files {
		if err := writeFileSync(file.path, file.data); err != nil {
			cleanup()
			return "", err
		}
	}
	if err := syncDirectory(contentDir); err != nil {
		cleanup()
		return "", err
	}
	if err := syncDirectory(temp); err != nil {
		cleanup()
		return "", err
	}
	destination := filepath.Join(s.root, artifacts.Proposal.TransactionID)
	if err := os.Rename(temp, destination); err != nil {
		cleanup()
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("transaction already exists: %s", artifacts.Proposal.TransactionID)
		}
		return "", fmt.Errorf("publish transaction: %w", err)
	}
	if err := syncDirectory(s.root); err != nil {
		return "", err
	}
	return digest, nil
}

func (s *Store) Load(transactionID string) (Artifacts, error) {
	dir, receipt, err := s.loadReceipt(transactionID)
	if err != nil {
		return Artifacts{}, err
	}
	artifacts := Artifacts{
		Proposal:      receipt.Proposal,
		State:         receipt.State,
		PreviewDigest: receipt.PreviewDigest,
		Retention:     receipt.Retention,
	}
	if receipt.State.Status == StatusDiscarded || receipt.Retention != nil {
		return artifacts, nil
	}
	lintBytes, err := readBounded(filepath.Join(dir, "lint.json"), maxArtifactBytes)
	if err != nil {
		return Artifacts{}, fmt.Errorf("read lint artifact: %w", err)
	}
	if Digest(lintBytes) != receipt.Proposal.LintSHA256 {
		return Artifacts{}, fmt.Errorf("lint artifact hash does not match proposal")
	}
	artifacts.Lint = lintBytes
	diffBytes, err := readBounded(filepath.Join(dir, "diff.patch"), maxArtifactBytes)
	if err != nil {
		return Artifacts{}, fmt.Errorf("read diff artifact: %w", err)
	}
	if Digest(diffBytes) != receipt.Proposal.DiffSHA256 {
		return Artifacts{}, fmt.Errorf("diff artifact hash does not match proposal")
	}
	artifacts.Diff = diffBytes
	artifacts.Contents = make([][]byte, len(receipt.Proposal.Operations))
	contentDir := filepath.Join(dir, "content")
	contentInfo, err := os.Lstat(contentDir)
	if err != nil {
		return Artifacts{}, fmt.Errorf("inspect content artifact directory: %w", err)
	}
	if contentInfo.Mode()&os.ModeSymlink != 0 || !contentInfo.IsDir() {
		return Artifacts{}, fmt.Errorf("content artifact path is not a regular directory")
	}
	for index, operation := range receipt.Proposal.Operations {
		if operation.Deleted {
			continue
		}
		content, readErr := readBounded(filepath.Join(dir, filepath.FromSlash(operation.ContentFile)), maxArtifactBytes)
		if readErr != nil {
			return Artifacts{}, fmt.Errorf("read content artifact %d: %w", index, readErr)
		}
		if Digest(content) != operation.ResultingContentSHA256 {
			return Artifacts{}, fmt.Errorf("content artifact %d hash does not match proposal", index)
		}
		artifacts.Contents[index] = content
	}
	return artifacts, nil
}

func (s *Store) LoadReceipt(transactionID string) (Receipt, error) {
	_, receipt, err := s.loadReceipt(transactionID)
	return receipt, err
}

func (s *Store) InspectPrune(transactionID string) (PruneInspection, error) {
	dir, receipt, err := s.loadReceipt(transactionID)
	if err != nil {
		return PruneInspection{}, err
	}
	if receipt.State.Status != StatusCommitted {
		return PruneInspection{}, fmt.Errorf("transaction in state %s cannot be pruned", receipt.State.Status)
	}
	inspection := PruneInspection{
		Proposal:      receipt.Proposal,
		State:         receipt.State,
		PreviewDigest: receipt.PreviewDigest,
		Retention:     receipt.Retention,
	}
	if receipt.Retention == nil {
		artifacts, err := s.Load(transactionID)
		if err != nil {
			return PruneInspection{}, err
		}
		if err := validatePruneLayout(dir, nil, &receipt.Proposal); err != nil {
			return PruneInspection{}, err
		}
		manifest := retentionManifest(artifacts)
		inspection.PayloadFiles, inspection.PayloadBytes = retentionArtifactTotals(manifest)
		inspection.RemainingPayloadFiles = inspection.PayloadFiles
		inspection.RemainingPayloadBytes = inspection.PayloadBytes
		return inspection, nil
	}
	if err := validatePruneLayout(dir, receipt.Retention, nil); err != nil {
		return PruneInspection{}, err
	}
	inspection.PayloadFiles, inspection.PayloadBytes = RetentionTotals(*receipt.Retention)
	remainingFiles, remainingBytes, err := inspectRetentionArtifacts(dir, *receipt.Retention)
	if err != nil {
		return PruneInspection{}, err
	}
	inspection.RemainingPayloadFiles = remainingFiles
	inspection.RemainingPayloadBytes = remainingBytes
	if receipt.Retention.Phase == RetentionPruned && remainingFiles != 0 {
		return PruneInspection{}, fmt.Errorf("pruned transaction still contains payload artifacts")
	}
	return inspection, nil
}

func (s *Store) Prune(ctx context.Context, transactionID, now string) (PruneResult, error) {
	if ctx == nil {
		return PruneResult{}, fmt.Errorf("transaction prune context is required")
	}
	if err := ctx.Err(); err != nil {
		return PruneResult{}, err
	}
	if _, err := parseRetentionTime(now); err != nil {
		return PruneResult{}, fmt.Errorf("transaction prune time must be RFC 3339")
	}
	inspection, err := s.InspectPrune(transactionID)
	if err != nil {
		return PruneResult{}, err
	}
	if inspection.Retention != nil && inspection.Retention.Phase == RetentionPruned {
		return PruneResult{Retention: *inspection.Retention, AlreadyPruned: true}, nil
	}
	dir, err := s.transactionDir(transactionID)
	if err != nil {
		return PruneResult{}, err
	}
	var receipt RetentionReceipt
	if inspection.Retention == nil {
		artifacts, err := s.Load(transactionID)
		if err != nil {
			return PruneResult{}, err
		}
		receipt = RetentionReceipt{
			SchemaVersion: SchemaVersion,
			TransactionID: transactionID,
			PreviewDigest: inspection.PreviewDigest,
			Phase:         RetentionPruning,
			StartedAt:     now,
			Artifacts:     retentionManifest(artifacts),
		}
		data, err := MarshalRetention(receipt)
		if err != nil {
			return PruneResult{}, err
		}
		if err := publishFileSync(s.root, filepath.Join(dir, "retention.json"), data); err != nil {
			return PruneResult{}, fmt.Errorf("write pruning receipt: %w", err)
		}
		if err := syncDirectory(dir); err != nil {
			return PruneResult{}, err
		}
		if err := s.runAfterPruneStep("retention.json:pruning"); err != nil {
			return PruneResult{}, err
		}
	} else {
		receipt = *inspection.Retention
	}
	if err := validatePruneLayout(dir, &receipt, nil); err != nil {
		return PruneResult{}, err
	}

	result := PruneResult{Retention: receipt}
	for _, artifact := range receipt.Artifacts {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		removed, bytesRemoved, err := removeRetentionArtifact(dir, artifact)
		if err != nil {
			return result, err
		}
		if !removed {
			continue
		}
		result.FilesRemoved++
		result.BytesRemoved += bytesRemoved
		if err := s.runAfterPruneStep(artifact.Path); err != nil {
			return result, err
		}
	}
	if err := removeEmptyContentDirectory(dir); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	remainingFiles, _, err := inspectRetentionArtifacts(dir, receipt)
	if err != nil {
		return result, err
	}
	if remainingFiles != 0 {
		return result, fmt.Errorf("transaction payload removal did not complete")
	}
	receipt.Phase = RetentionPruned
	receipt.CompletedAt = now
	data, err := MarshalRetention(receipt)
	if err != nil {
		return result, err
	}
	if err := replacePublishedFileSync(s.root, filepath.Join(dir, "retention.json"), data); err != nil {
		return result, fmt.Errorf("finalize pruning receipt: %w", err)
	}
	if err := validatePruneLayout(dir, &receipt, nil); err != nil {
		return result, err
	}
	if err := s.runAfterPruneStep("retention.json:pruned"); err != nil {
		return result, err
	}
	result.Retention = receipt
	return result, nil
}

func (s *Store) loadReceipt(transactionID string) (string, Receipt, error) {
	dir, err := s.transactionDir(transactionID)
	if err != nil {
		return "", Receipt{}, err
	}
	proposalBytes, err := readBounded(filepath.Join(dir, "proposal.json"), maxArtifactBytes)
	if err != nil {
		return "", Receipt{}, fmt.Errorf("read proposal: %w", err)
	}
	var proposal Proposal
	if err := decodeStrict(proposalBytes, &proposal); err != nil {
		return "", Receipt{}, fmt.Errorf("decode proposal: %w", err)
	}
	if err := ValidateProposal(proposal); err != nil {
		return "", Receipt{}, fmt.Errorf("validate proposal: %w", err)
	}
	if proposal.TransactionID != transactionID {
		return "", Receipt{}, fmt.Errorf("proposal transaction ID does not match directory")
	}
	stateBytes, err := readBounded(filepath.Join(dir, "state.json"), maxArtifactBytes)
	if err != nil {
		return "", Receipt{}, fmt.Errorf("read transaction state: %w", err)
	}
	var state State
	if err := decodeStrict(stateBytes, &state); err != nil {
		return "", Receipt{}, fmt.Errorf("decode transaction state: %w", err)
	}
	if err := ValidateState(state); err != nil {
		return "", Receipt{}, fmt.Errorf("validate transaction state: %w", err)
	}
	if state.TransactionID != transactionID {
		return "", Receipt{}, fmt.Errorf("state transaction ID does not match directory")
	}
	digest := Digest(proposalBytes)
	if !DigestEqual(digest, state.PreviewDigest) {
		return "", Receipt{}, fmt.Errorf("proposal artifact digest does not match transaction state")
	}
	retention, err := loadRetention(filepath.Join(dir, "retention.json"))
	if err != nil {
		return "", Receipt{}, err
	}
	if retention != nil {
		if state.Status != StatusCommitted {
			return "", Receipt{}, fmt.Errorf("only a committed transaction may have a retention receipt")
		}
		if err := ValidateRetentionForProposal(*retention, proposal, digest); err != nil {
			return "", Receipt{}, err
		}
	}
	return dir, Receipt{
		Proposal:      proposal,
		State:         state,
		PreviewDigest: digest,
		Retention:     retention,
	}, nil
}

func loadRetention(path string) (*RetentionReceipt, error) {
	data, err := readBounded(path, maxArtifactBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read retention receipt: %w", err)
	}
	receipt, err := DecodeRetention(data)
	if err != nil {
		return nil, err
	}
	return &receipt, nil
}

func retentionManifest(artifacts Artifacts) []RetentionArtifact {
	manifest := make([]RetentionArtifact, 0, len(artifacts.Contents)+2)
	manifest = append(manifest,
		RetentionArtifact{
			Path:   "diff.patch",
			SHA256: artifacts.Proposal.DiffSHA256,
			Bytes:  int64(len(artifacts.Diff)),
		},
		RetentionArtifact{
			Path:   "lint.json",
			SHA256: artifacts.Proposal.LintSHA256,
			Bytes:  int64(len(artifacts.Lint)),
		},
	)
	for index, content := range artifacts.Contents {
		if artifacts.Proposal.Operations[index].Deleted {
			continue
		}
		manifest = append(manifest, RetentionArtifact{
			Path:   artifacts.Proposal.Operations[index].ContentFile,
			SHA256: artifacts.Proposal.Operations[index].ResultingContentSHA256,
			Bytes:  int64(len(content)),
		})
	}
	sort.Slice(manifest, func(i, j int) bool { return manifest[i].Path < manifest[j].Path })
	return manifest
}

func retentionArtifactTotals(artifacts []RetentionArtifact) (int, int64) {
	var bytes int64
	for _, artifact := range artifacts {
		bytes += artifact.Bytes
	}
	return len(artifacts), bytes
}

func validatePruneLayout(dir string, retention *RetentionReceipt, proposal *Proposal) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("list transaction artifacts: %w", err)
	}
	allowed := map[string]bool{
		"proposal.json": true,
		"state.json":    true,
	}
	required := map[string]bool{
		"proposal.json": false,
		"state.json":    false,
	}
	switch {
	case retention == nil:
		for _, name := range []string{"content", "diff.patch", "lint.json"} {
			allowed[name] = true
			required[name] = false
		}
	case retention.Phase == RetentionPruning:
		for _, name := range []string{"content", "diff.patch", "lint.json", "retention.json"} {
			allowed[name] = true
		}
		required["retention.json"] = false
	case retention.Phase == RetentionPruned:
		allowed["retention.json"] = true
		required["retention.json"] = false
	}
	for _, entry := range entries {
		name := entry.Name()
		if !allowed[name] {
			return fmt.Errorf("unexpected transaction artifact %q", name)
		}
		info, err := os.Lstat(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("inspect transaction artifact %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("transaction artifact %q must not be a symlink", name)
		}
		if name == "content" {
			if !info.IsDir() {
				return fmt.Errorf("transaction content artifact must be a regular directory")
			}
		} else if !info.Mode().IsRegular() {
			return fmt.Errorf("transaction artifact %q must be a regular file", name)
		}
		if _, exists := required[name]; exists {
			required[name] = true
		}
	}
	for name, present := range required {
		if !present {
			return fmt.Errorf("required transaction artifact %q is missing", name)
		}
	}
	return validateContentLayout(dir, retention, proposal)
}

func validateContentLayout(dir string, retention *RetentionReceipt, proposal *Proposal) error {
	contentDir := filepath.Join(dir, "content")
	entries, err := os.ReadDir(contentDir)
	if errors.Is(err, fs.ErrNotExist) {
		if retention == nil {
			return fmt.Errorf("required transaction artifact %q is missing", "content")
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("list transaction content artifacts: %w", err)
	}
	expected := make(map[string]bool)
	if retention != nil {
		for _, artifact := range retention.Artifacts {
			if strings.HasPrefix(artifact.Path, "content/") {
				expected[filepath.Base(artifact.Path)] = false
			}
		}
	} else if proposal != nil {
		for _, operation := range proposal.Operations {
			if operation.Deleted {
				continue
			}
			expected[filepath.Base(operation.ContentFile)] = false
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, exists := expected[name]; !exists {
			return fmt.Errorf("unexpected transaction content artifact %q", name)
		}
		info, err := os.Lstat(filepath.Join(contentDir, name))
		if err != nil {
			return fmt.Errorf("inspect transaction content artifact %q: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("transaction content artifact %q must be a regular non-symlink file", name)
		}
		expected[name] = true
	}
	if retention == nil {
		for name, present := range expected {
			if !present {
				return fmt.Errorf("required transaction content artifact %q is missing", name)
			}
		}
	}
	return nil
}

func inspectRetentionArtifacts(dir string, receipt RetentionReceipt) (int, int64, error) {
	var files int
	var bytes int64
	for _, artifact := range receipt.Artifacts {
		artifactPath := filepath.Join(dir, filepath.FromSlash(artifact.Path))
		data, err := readBounded(artifactPath, maxArtifactBytes)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, 0, fmt.Errorf("inspect retained artifact %q: %w", artifact.Path, err)
		}
		if int64(len(data)) != artifact.Bytes || !DigestEqual(Digest(data), artifact.SHA256) {
			return 0, 0, fmt.Errorf("retained artifact %q does not match retention receipt", artifact.Path)
		}
		files++
		bytes += int64(len(data))
	}
	return files, bytes, nil
}

func removeRetentionArtifact(dir string, artifact RetentionArtifact) (bool, int64, error) {
	artifactPath := filepath.Join(dir, filepath.FromSlash(artifact.Path))
	data, err := readBounded(artifactPath, maxArtifactBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("read pruned artifact %q: %w", artifact.Path, err)
	}
	if int64(len(data)) != artifact.Bytes || !DigestEqual(Digest(data), artifact.SHA256) {
		return false, 0, fmt.Errorf("pruned artifact %q does not match retention receipt", artifact.Path)
	}
	if err := os.Remove(artifactPath); err != nil {
		return false, 0, fmt.Errorf("remove pruned artifact %q: %w", artifact.Path, err)
	}
	if err := syncDirectory(filepath.Dir(artifactPath)); err != nil {
		return false, 0, err
	}
	return true, int64(len(data)), nil
}

func removeEmptyContentDirectory(dir string) error {
	contentDir := filepath.Join(dir, "content")
	info, err := os.Lstat(contentDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect pruned content directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("pruned content path is not a regular directory")
	}
	entries, err := os.ReadDir(contentDir)
	if err != nil {
		return fmt.Errorf("list pruned content directory: %w", err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("pruned content directory contains unexpected artifacts")
	}
	if err := os.Remove(contentDir); err != nil {
		return fmt.Errorf("remove pruned content directory: %w", err)
	}
	return syncDirectory(dir)
}

func (s *Store) runAfterPruneStep(step string) error {
	if s.afterPruneStep == nil {
		return nil
	}
	return s.afterPruneStep(step)
}

func parseRetentionTime(value string) (string, error) {
	if _, err := time.Parse(time.RFC3339Nano, value); err != nil {
		return "", err
	}
	return value, nil
}

func (s *Store) UpdateState(transactionID string, next State) error {
	current, err := s.loadState(transactionID)
	if err != nil {
		return err
	}
	if next.TransactionID != transactionID || next.PreviewDigest != current.PreviewDigest {
		return fmt.Errorf("updated state identity does not match stored state")
	}
	if err := ValidateTransition(current.Status, next.Status); err != nil {
		return err
	}
	data, err := MarshalState(next)
	if err != nil {
		return err
	}
	return replaceFile(filepath.Join(s.root, transactionID, "state.json"), data)
}

func (s *Store) Discard(transactionID string, updatedAt string) (State, error) {
	artifacts, err := s.Load(transactionID)
	if err != nil {
		return State{}, err
	}
	if artifacts.State.Status == StatusDiscarded {
		if err := s.removeDiscardedArtifacts(transactionID); err != nil {
			return State{}, err
		}
		return artifacts.State, nil
	}
	if artifacts.State.Status != StatusPreviewed && artifacts.State.Status != StatusFailed {
		return State{}, fmt.Errorf("transaction in state %s cannot be discarded", artifacts.State.Status)
	}
	next := artifacts.State
	next.Status = StatusDiscarded
	next.UpdatedAt = updatedAt
	if err := s.UpdateState(transactionID, next); err != nil {
		return State{}, err
	}
	if err := s.removeDiscardedArtifacts(transactionID); err != nil {
		return State{}, err
	}
	return next, nil
}

func (s *Store) ListIDs() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".tmp-") {
			continue
		}
		if ValidateTransactionID(entry.Name()) == nil {
			ids = append(ids, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	return ids, nil
}

func (s *Store) ListIDsStrict() ([]string, error) {
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, fs.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	var ids []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".tmp-") || ValidateTransactionID(name) != nil {
			continue
		}
		info, err := os.Lstat(filepath.Join(s.root, name))
		if err != nil {
			return nil, fmt.Errorf("inspect transaction %s: %w", name, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, fmt.Errorf("transaction %s is not a regular directory", name)
		}
		ids = append(ids, name)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) loadState(transactionID string) (State, error) {
	dir, err := s.transactionDir(transactionID)
	if err != nil {
		return State{}, err
	}
	data, err := readBounded(filepath.Join(dir, "state.json"), maxArtifactBytes)
	if err != nil {
		return State{}, fmt.Errorf("read transaction state: %w", err)
	}
	var state State
	if err := decodeStrict(data, &state); err != nil {
		return State{}, fmt.Errorf("decode transaction state: %w", err)
	}
	if err := ValidateState(state); err != nil {
		return State{}, err
	}
	if state.TransactionID != transactionID {
		return State{}, fmt.Errorf("state transaction ID does not match directory")
	}
	return state, nil
}

func (s *Store) LoadState(transactionID string) (State, error) {
	return s.loadState(transactionID)
}

func (s *Store) transactionDir(transactionID string) (string, error) {
	if err := ValidateTransactionID(transactionID); err != nil {
		return "", err
	}
	dir := filepath.Join(s.root, transactionID)
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("transaction path is not a regular directory")
	}
	return dir, nil
}

func (s *Store) removeDiscardedArtifacts(transactionID string) error {
	dir, err := s.transactionDir(transactionID)
	if err != nil {
		return err
	}
	contentDir := filepath.Join(dir, "content")
	if info, statErr := os.Lstat(contentDir); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("transaction content path is not a regular directory")
		}
		if err := os.RemoveAll(contentDir); err != nil {
			return fmt.Errorf("remove discarded transaction content: %w", err)
		}
	} else if !errors.Is(statErr, fs.ErrNotExist) {
		return fmt.Errorf("inspect discarded transaction content: %w", statErr)
	}
	if err := os.Remove(filepath.Join(dir, "diff.patch")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove discarded transaction diff: %w", err)
	}
	if err := os.Remove(filepath.Join(dir, "lint.json")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove discarded transaction lint: %w", err)
	}
	return syncDirectory(dir)
}

func writeFileSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create transaction artifact: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write transaction artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush transaction artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transaction artifact: %w", err)
	}
	return nil
}

func publishFileSync(tempDir, destination string, data []byte) error {
	temp, err := os.CreateTemp(tempDir, ".retention-")
	if err != nil {
		return fmt.Errorf("create retention temporary file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set retention temporary file permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write retention temporary file: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush retention temporary file: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close retention temporary file: %w", err)
	}
	if err := os.Link(tempPath, destination); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("publish retention receipt: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("remove retention temporary link: %w", err)
	}
	return syncDirectory(tempDir)
}

func replacePublishedFileSync(tempDir, destination string, data []byte) error {
	temp, err := os.CreateTemp(tempDir, ".retention-")
	if err != nil {
		return fmt.Errorf("create retention replacement file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set retention replacement permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write retention replacement: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush retention replacement: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close retention replacement: %w", err)
	}
	if err := os.Rename(tempPath, destination); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace retention receipt: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return err
	}
	return syncDirectory(tempDir)
}

func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".state-")
	if err != nil {
		return fmt.Errorf("create state temporary file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("replace transaction state: %w", err)
	}
	return syncDirectory(dir)
}

func readBounded(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact is not a regular non-symlink file")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("artifact changed while it was opened")
	}
	currentInfo, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if currentInfo.Mode()&os.ModeSymlink != 0 ||
		!currentInfo.Mode().IsRegular() ||
		!os.SameFile(openedInfo, currentInfo) {
		return nil, fmt.Errorf("artifact changed after it was opened")
	}
	data, err := ioReadAllLimit(file, limit)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func ioReadAllLimit(file *os.File, limit int64) ([]byte, error) {
	reader := io.LimitReader(file, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact exceeds %d bytes", limit)
	}
	return data, nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("sync directory: %w", err)
	}
	return nil
}
