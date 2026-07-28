package recovery

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"lore/internal/docs"
	"lore/internal/repository"
	"lore/internal/transaction"
)

const SchemaVersion = 1

type Phase string

const (
	PhasePrepared      Phase = "prepared"
	PhaseApplyingFiles Phase = "applying_files"
	PhaseFilesApplied  Phase = "files_applied"
	PhaseGitCommitted  Phase = "git_committed"
	PhaseFinalized     Phase = "finalized"
)

type Process struct {
	PID      int    `json:"pid"`
	Hostname string `json:"hostname"`
	Command  string `json:"command"`
}

type File struct {
	Path              string `json:"path"`
	OriginalExists    bool   `json:"original_exists"`
	OriginalRevision  string `json:"original_revision,omitempty"`
	OriginalContent   string `json:"original_content,omitempty"`
	ResultingRevision string `json:"resulting_revision"`
	Applied           bool   `json:"applied"`
}

type Journal struct {
	SchemaVersion int      `json:"schema_version"`
	TransactionID string   `json:"transaction_id"`
	PreviewDigest string   `json:"preview_digest"`
	Phase         Phase    `json:"phase"`
	BaseCommit    string   `json:"base_commit"`
	BaseBranch    string   `json:"base_branch"`
	ChangedPaths  []string `json:"changed_paths"`
	Files         []File   `json:"files"`
	StartedAt     string   `json:"started_at"`
	Process       Process  `json:"process"`
	Commit        string   `json:"commit,omitempty"`
}

type Store struct {
	repo   *repository.Repository
	parent string
	active string
}

func NewStore(repo *repository.Repository) (*Store, error) {
	parent, err := repo.SafeRepositoryPath(".lore/recovery")
	if err != nil {
		return nil, fmt.Errorf("resolve recovery store: %w", err)
	}
	return &Store{
		repo: repo, parent: parent, active: filepath.Join(parent, "active"),
	}, nil
}

func NewJournal(proposal transaction.Proposal, previewDigest string, originals [][]byte, originalExists []bool, now time.Time, command string) (Journal, error) {
	if len(originals) != len(proposal.Operations) || len(originalExists) != len(proposal.Operations) {
		return Journal{}, fmt.Errorf("journal originals do not match proposal operations")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return Journal{}, fmt.Errorf("read hostname: %w", err)
	}
	journal := Journal{
		SchemaVersion: SchemaVersion,
		TransactionID: proposal.TransactionID,
		PreviewDigest: previewDigest,
		Phase:         PhasePrepared,
		BaseCommit:    proposal.BaseCommit,
		BaseBranch:    proposal.BaseBranch,
		ChangedPaths:  append([]string(nil), proposal.ChangedPaths...),
		Files:         make([]File, len(proposal.Operations)),
		StartedAt:     now.UTC().Format(time.RFC3339Nano),
		Process: Process{
			PID: os.Getpid(), Hostname: hostname, Command: command,
		},
	}
	for index, operation := range proposal.Operations {
		entry := File{
			Path:              operation.Path,
			OriginalExists:    originalExists[index],
			ResultingRevision: operation.ResultingContentSHA256,
		}
		if originalExists[index] {
			entry.OriginalRevision = docs.Revision(originals[index])
			entry.OriginalContent = fmt.Sprintf("originals/%03d.md", index)
		}
		journal.Files[index] = entry
	}
	if err := Validate(journal); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (s *Store) Create(journal Journal, originals [][]byte) error {
	if err := Validate(journal); err != nil {
		return err
	}
	if len(originals) != len(journal.Files) {
		return fmt.Errorf("recovery originals do not match journal files")
	}
	if err := os.MkdirAll(s.parent, 0o700); err != nil {
		return fmt.Errorf("create recovery store: %w", err)
	}
	if err := os.Chmod(s.parent, 0o700); err != nil {
		return fmt.Errorf("set recovery store permissions: %w", err)
	}
	if _, err := os.Lstat(s.active); err == nil {
		return fmt.Errorf("an active recovery journal already exists")
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect active recovery journal: %w", err)
	}
	temp, err := os.MkdirTemp(s.parent, ".active-")
	if err != nil {
		return fmt.Errorf("create recovery temporary directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(temp) }
	if err := os.Chmod(temp, 0o700); err != nil {
		cleanup()
		return err
	}
	originalsDir := filepath.Join(temp, "originals")
	if err := os.Mkdir(originalsDir, 0o700); err != nil {
		cleanup()
		return fmt.Errorf("create recovery originals directory: %w", err)
	}
	for index, file := range journal.Files {
		if !file.OriginalExists {
			continue
		}
		if docs.Revision(originals[index]) != file.OriginalRevision {
			cleanup()
			return fmt.Errorf("recovery original %d does not match journal revision", index)
		}
		if err := writeSync(filepath.Join(temp, filepath.FromSlash(file.OriginalContent)), originals[index]); err != nil {
			cleanup()
			return err
		}
	}
	data, err := Marshal(journal)
	if err != nil {
		cleanup()
		return err
	}
	if err := writeSync(filepath.Join(temp, "journal.json"), data); err != nil {
		cleanup()
		return err
	}
	if err := syncDir(originalsDir); err != nil {
		cleanup()
		return err
	}
	if err := syncDir(temp); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(temp, s.active); err != nil {
		cleanup()
		return fmt.Errorf("publish recovery journal: %w", err)
	}
	return syncDir(s.parent)
}

func (s *Store) Load() (Journal, [][]byte, error) {
	activeInfo, err := os.Lstat(s.active)
	if err != nil {
		return Journal{}, nil, err
	}
	if activeInfo.Mode()&os.ModeSymlink != 0 || !activeInfo.IsDir() {
		return Journal{}, nil, fmt.Errorf("active recovery path is not a regular directory")
	}
	data, err := readRegular(filepath.Join(s.active, "journal.json"), transaction.MaxRequestBytes)
	if err != nil {
		return Journal{}, nil, fmt.Errorf("read recovery journal: %w", err)
	}
	var journal Journal
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return Journal{}, nil, fmt.Errorf("decode recovery journal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Journal{}, nil, fmt.Errorf("recovery journal contains multiple JSON values")
	}
	if err := Validate(journal); err != nil {
		return Journal{}, nil, err
	}
	originals := make([][]byte, len(journal.Files))
	originalsDir := filepath.Join(s.active, "originals")
	info, err := os.Lstat(originalsDir)
	if err != nil {
		return Journal{}, nil, fmt.Errorf("inspect recovery originals: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return Journal{}, nil, fmt.Errorf("recovery originals path is not a regular directory")
	}
	for index, file := range journal.Files {
		if !file.OriginalExists {
			continue
		}
		content, err := readRegular(filepath.Join(s.active, filepath.FromSlash(file.OriginalContent)), transaction.MaxRequestBytes)
		if err != nil {
			return Journal{}, nil, fmt.Errorf("read recovery original %d: %w", index, err)
		}
		if docs.Revision(content) != file.OriginalRevision {
			return Journal{}, nil, fmt.Errorf("recovery original %d revision does not match journal", index)
		}
		originals[index] = content
	}
	return journal, originals, nil
}

func (s *Store) Update(journal Journal) error {
	current, _, err := s.Load()
	if err != nil {
		return err
	}
	if current.TransactionID != journal.TransactionID || current.PreviewDigest != journal.PreviewDigest {
		return fmt.Errorf("recovery journal identity cannot change")
	}
	if err := ValidatePhaseTransition(current.Phase, journal.Phase); err != nil {
		return err
	}
	if err := Validate(journal); err != nil {
		return err
	}
	data, err := Marshal(journal)
	if err != nil {
		return err
	}
	return replaceSync(filepath.Join(s.active, "journal.json"), data)
}

func (s *Store) Remove() error {
	journal, _, err := s.Load()
	if err != nil {
		return err
	}
	if journal.Phase != PhaseFinalized {
		return fmt.Errorf("recovery journal must be finalized before removal")
	}
	if err := os.RemoveAll(s.active); err != nil {
		return fmt.Errorf("remove recovery journal: %w", err)
	}
	return syncDir(s.parent)
}

func (s *Store) RemoveForRollback() error {
	journal, _, err := s.Load()
	if err != nil {
		return err
	}
	if journal.Phase == PhaseGitCommitted || journal.Phase == PhaseFinalized {
		return fmt.Errorf("a Git-committed recovery journal cannot be removed as a rollback")
	}
	if err := os.RemoveAll(s.active); err != nil {
		return fmt.Errorf("remove rolled-back recovery journal: %w", err)
	}
	return syncDir(s.parent)
}

func Marshal(journal Journal) ([]byte, error) {
	if err := Validate(journal); err != nil {
		return nil, err
	}
	data, err := json.Marshal(journal)
	if err != nil {
		return nil, fmt.Errorf("marshal recovery journal: %w", err)
	}
	return append(data, '\n'), nil
}

func Validate(journal Journal) error {
	if journal.SchemaVersion != SchemaVersion {
		return fmt.Errorf("recovery schema_version must equal %d", SchemaVersion)
	}
	if err := transaction.ValidateTransactionID(journal.TransactionID); err != nil {
		return err
	}
	if err := transaction.ValidateRevision(journal.PreviewDigest); err != nil {
		return fmt.Errorf("invalid recovery preview digest")
	}
	switch journal.Phase {
	case PhasePrepared, PhaseApplyingFiles, PhaseFilesApplied, PhaseGitCommitted, PhaseFinalized:
	default:
		return fmt.Errorf("invalid recovery phase %q", journal.Phase)
	}
	if journal.BaseCommit == "" || journal.BaseBranch == "" {
		return fmt.Errorf("recovery base state is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, journal.StartedAt); err != nil {
		return fmt.Errorf("recovery started_at must be RFC 3339")
	}
	if journal.Process.PID <= 0 || journal.Process.Hostname == "" || journal.Process.Command == "" {
		return fmt.Errorf("recovery process metadata is incomplete")
	}
	if len(journal.Files) == 0 || len(journal.Files) != len(journal.ChangedPaths) {
		return fmt.Errorf("recovery files must correspond to changed paths")
	}
	for index, path := range journal.ChangedPaths {
		if index > 0 && journal.ChangedPaths[index-1] >= path {
			return fmt.Errorf("recovery changed paths must be unique and sorted")
		}
	}
	for index, file := range journal.Files {
		if file.Path != journal.ChangedPaths[index] {
			return fmt.Errorf("recovery file order does not match changed paths")
		}
		if _, err := repositoryPath(file.Path); err != nil {
			return err
		}
		if err := transaction.ValidateRevision(file.ResultingRevision); err != nil {
			return fmt.Errorf("invalid resulting revision for %s", file.Path)
		}
		if file.OriginalExists {
			if err := transaction.ValidateRevision(file.OriginalRevision); err != nil {
				return fmt.Errorf("invalid original revision for %s", file.Path)
			}
			if file.OriginalContent != fmt.Sprintf("originals/%03d.md", index) {
				return fmt.Errorf("invalid original content location for %s", file.Path)
			}
		} else if file.OriginalRevision != "" || file.OriginalContent != "" {
			return fmt.Errorf("absent original for %s must not have content metadata", file.Path)
		}
	}
	if (journal.Phase == PhaseGitCommitted || journal.Phase == PhaseFinalized) && journal.Commit == "" {
		return fmt.Errorf("recovery phase %s requires a commit hash", journal.Phase)
	}
	return nil
}

func ValidatePhaseTransition(from, to Phase) error {
	if from == to {
		return nil
	}
	allowed := map[Phase]Phase{
		PhasePrepared:      PhaseApplyingFiles,
		PhaseApplyingFiles: PhaseFilesApplied,
		PhaseFilesApplied:  PhaseGitCommitted,
		PhaseGitCommitted:  PhaseFinalized,
	}
	if allowed[from] != to {
		return fmt.Errorf("invalid recovery phase transition from %s to %s", from, to)
	}
	return nil
}

func repositoryPath(path string) (string, error) {
	if err := transaction.ValidatePagePath(path); err == nil {
		return path, nil
	}
	if err := transaction.ValidateSourcePath(path); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("invalid recovery content path %q", path)
}

func writeSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create recovery artifact: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write recovery artifact: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("flush recovery artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close recovery artifact: %w", err)
	}
	return nil
}

func replaceSync(path string, data []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".journal-")
	if err != nil {
		return err
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
		return err
	}
	return syncDir(dir)
}

func readRegular(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("recovery artifact is not a regular non-symlink file")
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("recovery artifact exceeds %d bytes", limit)
	}
	return os.ReadFile(path)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
