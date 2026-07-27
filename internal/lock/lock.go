package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const directoryName = "write.lock"

type Metadata struct {
	PID       int       `json:"pid"`
	Hostname  string    `json:"hostname"`
	Command   string    `json:"command"`
	StartedAt time.Time `json:"started_at"`
}

type ContentionError struct {
	Path     string
	Metadata Metadata
	Cause    error
}

func (e *ContentionError) Error() string {
	return fmt.Sprintf("repository write lock is held at %s by pid %d on %s for %s since %s",
		e.Path, e.Metadata.PID, e.Metadata.Hostname, e.Metadata.Command, e.Metadata.StartedAt.Format(time.RFC3339))
}

func (e *ContentionError) Unwrap() error {
	return e.Cause
}

type Handle struct {
	directory string
	released  bool
}

func Acquire(repoRoot, command string, now time.Time) (*Handle, error) {
	runtimeDir := filepath.Join(repoRoot, ".lore")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create Lore runtime directory: %w", err)
	}
	lockDir := filepath.Join(runtimeDir, directoryName)
	if err := os.Mkdir(lockDir, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			metadata, readErr := readMetadata(lockDir)
			return nil, &ContentionError{
				Path:     lockDir,
				Metadata: metadata,
				Cause:    readErr,
			}
		}
		return nil, fmt.Errorf("acquire repository write lock: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		_ = os.Remove(lockDir)
		return nil, fmt.Errorf("read hostname for repository lock: %w", err)
	}
	metadata := Metadata{
		PID:       os.Getpid(),
		Hostname:  hostname,
		Command:   command,
		StartedAt: now.UTC(),
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		_ = os.Remove(lockDir)
		return nil, fmt.Errorf("encode repository lock metadata: %w", err)
	}
	data = append(data, '\n')
	metadataPath := filepath.Join(lockDir, "metadata.json")
	if err := os.WriteFile(metadataPath, data, 0o600); err != nil {
		_ = os.Remove(lockDir)
		return nil, fmt.Errorf("write repository lock metadata: %w", err)
	}
	return &Handle{directory: lockDir}, nil
}

func (h *Handle) Release() error {
	if h == nil || h.released {
		return nil
	}
	metadataPath := filepath.Join(h.directory, "metadata.json")
	if err := os.Remove(metadataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove repository lock metadata: %w", err)
	}
	if err := os.Remove(h.directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("release repository write lock: %w", err)
	}
	h.released = true
	return nil
}

func ManualRecoveryPath(repoRoot string) string {
	return filepath.Join(repoRoot, ".lore", directoryName)
}

func readMetadata(lockDir string) (Metadata, error) {
	data, err := os.ReadFile(filepath.Join(lockDir, "metadata.json"))
	if err != nil {
		return Metadata{}, fmt.Errorf("read lock metadata: %w", err)
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("parse lock metadata: %w", err)
	}
	return metadata, nil
}
