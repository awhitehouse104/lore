package transaction

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"lore/internal/repository"
)

const maxArtifactBytes = MaxDiffBytes + 1

type Artifacts struct {
	Proposal      Proposal
	State         State
	PreviewDigest string
	Diff          []byte
	Lint          []byte
	Contents      [][]byte
}

type Store struct {
	repo *repository.Repository
	root string
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
		if Digest(content) != artifacts.Proposal.Operations[index].ResultingContentSHA256 {
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
	dir, err := s.transactionDir(transactionID)
	if err != nil {
		return Artifacts{}, err
	}
	proposalBytes, err := readBounded(filepath.Join(dir, "proposal.json"), maxArtifactBytes)
	if err != nil {
		return Artifacts{}, fmt.Errorf("read proposal: %w", err)
	}
	var proposal Proposal
	if err := decodeStrict(proposalBytes, &proposal); err != nil {
		return Artifacts{}, fmt.Errorf("decode proposal: %w", err)
	}
	if err := ValidateProposal(proposal); err != nil {
		return Artifacts{}, fmt.Errorf("validate proposal: %w", err)
	}
	if proposal.TransactionID != transactionID {
		return Artifacts{}, fmt.Errorf("proposal transaction ID does not match directory")
	}
	stateBytes, err := readBounded(filepath.Join(dir, "state.json"), maxArtifactBytes)
	if err != nil {
		return Artifacts{}, fmt.Errorf("read transaction state: %w", err)
	}
	var state State
	if err := decodeStrict(stateBytes, &state); err != nil {
		return Artifacts{}, fmt.Errorf("decode transaction state: %w", err)
	}
	if err := ValidateState(state); err != nil {
		return Artifacts{}, fmt.Errorf("validate transaction state: %w", err)
	}
	if state.TransactionID != transactionID {
		return Artifacts{}, fmt.Errorf("state transaction ID does not match directory")
	}
	digest := Digest(proposalBytes)
	if !DigestEqual(digest, state.PreviewDigest) {
		return Artifacts{}, fmt.Errorf("proposal artifact digest does not match transaction state")
	}
	artifacts := Artifacts{
		Proposal:      proposal,
		State:         state,
		PreviewDigest: digest,
	}
	lintBytes, err := readBounded(filepath.Join(dir, "lint.json"), maxArtifactBytes)
	if err != nil {
		return Artifacts{}, fmt.Errorf("read lint artifact: %w", err)
	}
	if Digest(lintBytes) != proposal.LintSHA256 {
		return Artifacts{}, fmt.Errorf("lint artifact hash does not match proposal")
	}
	artifacts.Lint = lintBytes
	if state.Status == StatusDiscarded {
		return artifacts, nil
	}
	diffBytes, err := readBounded(filepath.Join(dir, "diff.patch"), maxArtifactBytes)
	if err != nil {
		return Artifacts{}, fmt.Errorf("read diff artifact: %w", err)
	}
	if Digest(diffBytes) != proposal.DiffSHA256 {
		return Artifacts{}, fmt.Errorf("diff artifact hash does not match proposal")
	}
	artifacts.Diff = diffBytes
	artifacts.Contents = make([][]byte, len(proposal.Operations))
	contentDir := filepath.Join(dir, "content")
	contentInfo, err := os.Lstat(contentDir)
	if err != nil {
		return Artifacts{}, fmt.Errorf("inspect content artifact directory: %w", err)
	}
	if contentInfo.Mode()&os.ModeSymlink != 0 || !contentInfo.IsDir() {
		return Artifacts{}, fmt.Errorf("content artifact path is not a regular directory")
	}
	for index, operation := range proposal.Operations {
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
	return state, nil
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
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
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
