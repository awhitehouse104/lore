package lock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

const (
	fileName              = "write.lock"
	metadataSchemaVersion = 1
	maximumMetadataBytes  = 4 * 1024
	initialRetryDelay     = 10 * time.Millisecond
	maximumRetryDelay     = 100 * time.Millisecond
)

type Metadata struct {
	SchemaVersion int       `json:"schema_version"`
	PID           int       `json:"pid"`
	Hostname      string    `json:"hostname"`
	Command       string    `json:"command"`
	StartedAt     time.Time `json:"started_at"`
}

type ContentionError struct {
	Path              string
	Metadata          Metadata
	MetadataAvailable bool
	LegacyDirectory   bool
	Waited            time.Duration
	Cause             error
}

func (e *ContentionError) Error() string {
	description := "repository write lock is held"
	if e.LegacyDirectory {
		description = "legacy repository write lock directory exists"
	}
	if !e.MetadataAvailable {
		return fmt.Sprintf("%s at %s; owner metadata is unavailable", description, e.Path)
	}
	return fmt.Sprintf("%s at %s by pid %d on %s for %s since %s",
		description,
		e.Path,
		e.Metadata.PID,
		e.Metadata.Hostname,
		e.Metadata.Command,
		e.Metadata.StartedAt.Format(time.RFC3339),
	)
}

func (e *ContentionError) Unwrap() error {
	return e.Cause
}

type Handle struct {
	file     *os.File
	released bool
}

func Acquire(
	ctx context.Context,
	repoRoot string,
	command string,
	now time.Time,
	maximumWait time.Duration,
) (*Handle, error) {
	if ctx == nil {
		return nil, fmt.Errorf("repository write lock context is required")
	}
	if maximumWait < 0 {
		return nil, fmt.Errorf("repository write lock wait must not be negative")
	}
	if !validMetadataText(command, 128) {
		return nil, fmt.Errorf("repository write lock command is invalid")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	runtimeDir, err := ensureRuntimeDirectory(repoRoot)
	if err != nil {
		return nil, err
	}
	lockPath := filepath.Join(runtimeDir, fileName)
	startedWaiting := time.Now()
	deadline := startedWaiting.Add(maximumWait)
	retryDelay := initialRetryDelay
	var lastContention *ContentionError

	for {
		handle, contention, err := tryAcquire(lockPath, command, now)
		if err != nil {
			return nil, err
		}
		if handle != nil {
			return handle, nil
		}
		lastContention = contention
		lastContention.Waited = time.Since(startedWaiting)

		if maximumWait == 0 || !time.Now().Before(deadline) {
			lastContention.Waited = maximumWait
			return nil, lastContention
		}
		wait := min(retryDelay, time.Until(deadline))
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
		retryDelay = min(retryDelay*2, maximumRetryDelay)
	}
}

func Path(repoRoot string) string {
	return filepath.Join(repoRoot, ".lore", fileName)
}

func (h *Handle) Release() error {
	if h == nil || h.released {
		return nil
	}
	h.released = true
	if h.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	closeErr := h.file.Close()
	h.file = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock repository write lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close repository write lock: %w", closeErr)
	}
	return nil
}

func ensureRuntimeDirectory(repoRoot string) (string, error) {
	runtimeDir := filepath.Join(repoRoot, ".lore")
	info, err := os.Lstat(runtimeDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect Lore runtime directory: %w", err)
		}
		if err := os.Mkdir(runtimeDir, 0o700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return "", fmt.Errorf("create Lore runtime directory: %w", err)
			}
			info, err = os.Lstat(runtimeDir)
			if err != nil {
				return "", fmt.Errorf("inspect concurrently created Lore runtime directory: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", fmt.Errorf("Lore runtime path must be a real directory, not a symlink or non-directory")
			}
		}
		return runtimeDir, nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("Lore runtime path must be a real directory, not a symlink or non-directory")
	}
	return runtimeDir, nil
}

func tryAcquire(lockPath, command string, now time.Time) (*Handle, *ContentionError, error) {
	info, err := os.Lstat(lockPath)
	if err == nil {
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return nil, nil, fmt.Errorf("repository write lock path must not be a symlink")
		case info.IsDir():
			return nil, legacyContention(lockPath), nil
		case !info.Mode().IsRegular():
			return nil, nil, fmt.Errorf("repository write lock path must be a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("inspect repository write lock: %w", err)
	}

	file, err := os.OpenFile(
		lockPath,
		os.O_RDWR|os.O_CREATE|syscall.O_CLOEXEC|syscall.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		if current, statErr := os.Lstat(lockPath); statErr == nil && current.IsDir() {
			return nil, legacyContention(lockPath), nil
		}
		return nil, nil, fmt.Errorf("open repository write lock: %w", err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("inspect opened repository write lock: %w", err)
	}
	if !fileInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("repository write lock path must remain a regular file")
	}

	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, nil, fmt.Errorf("acquire repository write lock: %w", err)
		}
		metadata, metadataErr := readMetadataFile(file)
		closeErr := file.Close()
		if metadataErr == nil && closeErr != nil {
			metadataErr = fmt.Errorf("close contended repository write lock: %w", closeErr)
		}
		return nil, &ContentionError{
			Path:              lockPath,
			Metadata:          metadata,
			MetadataAvailable: metadataErr == nil,
			Cause:             metadataErr,
		}, nil
	}

	if err := file.Chmod(0o600); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, nil, fmt.Errorf("set repository write lock permissions: %w", err)
	}
	hostname, err := os.Hostname()
	if err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, nil, fmt.Errorf("read hostname for repository lock: %w", err)
	}
	metadata := Metadata{
		SchemaVersion: metadataSchemaVersion,
		PID:           os.Getpid(),
		Hostname:      hostname,
		Command:       command,
		StartedAt:     now.UTC(),
	}
	if err := validateMetadata(metadata, false); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, nil, fmt.Errorf("construct repository lock metadata: %w", err)
	}
	if err := writeMetadataFile(file, metadata); err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, nil, err
	}
	return &Handle{file: file}, nil, nil
}

func legacyContention(lockDir string) *ContentionError {
	metadata, err := readLegacyMetadata(lockDir)
	return &ContentionError{
		Path:              lockDir,
		Metadata:          metadata,
		MetadataAvailable: err == nil,
		LegacyDirectory:   true,
		Cause:             err,
	}
}

func writeMetadataFile(file *os.File, metadata Metadata) error {
	data, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode repository lock metadata: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maximumMetadataBytes {
		return fmt.Errorf("repository lock metadata exceeds %d bytes", maximumMetadataBytes)
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate repository lock metadata: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek repository lock metadata: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write repository lock metadata: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush repository lock metadata: %w", err)
	}
	return nil
}

func readMetadataFile(file *os.File) (Metadata, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Metadata{}, fmt.Errorf("seek repository lock metadata: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumMetadataBytes+1))
	if err != nil {
		return Metadata{}, fmt.Errorf("read repository lock metadata: %w", err)
	}
	if len(data) > maximumMetadataBytes {
		return Metadata{}, fmt.Errorf("repository lock metadata exceeds %d bytes", maximumMetadataBytes)
	}
	var metadata Metadata
	if err := decodeStrict(data, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("parse repository lock metadata: %w", err)
	}
	if err := validateMetadata(metadata, false); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func readLegacyMetadata(lockDir string) (Metadata, error) {
	metadataPath := filepath.Join(lockDir, "metadata.json")
	info, err := os.Lstat(metadataPath)
	if err != nil {
		return Metadata{}, fmt.Errorf("inspect legacy lock metadata: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > maximumMetadataBytes {
		return Metadata{}, fmt.Errorf("legacy lock metadata must be a bounded regular file")
	}
	file, err := os.OpenFile(metadataPath, os.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Metadata{}, fmt.Errorf("open legacy lock metadata: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maximumMetadataBytes+1))
	if err != nil {
		return Metadata{}, fmt.Errorf("read legacy lock metadata: %w", err)
	}
	if len(data) > maximumMetadataBytes {
		return Metadata{}, fmt.Errorf("legacy lock metadata exceeds %d bytes", maximumMetadataBytes)
	}
	var legacy struct {
		PID       int       `json:"pid"`
		Hostname  string    `json:"hostname"`
		Command   string    `json:"command"`
		StartedAt time.Time `json:"started_at"`
	}
	if err := decodeStrict(data, &legacy); err != nil {
		return Metadata{}, fmt.Errorf("parse legacy lock metadata: %w", err)
	}
	metadata := Metadata{
		PID:       legacy.PID,
		Hostname:  legacy.Hostname,
		Command:   legacy.Command,
		StartedAt: legacy.StartedAt,
	}
	if err := validateMetadata(metadata, true); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func validateMetadata(metadata Metadata, legacy bool) error {
	if !legacy && metadata.SchemaVersion != metadataSchemaVersion {
		return fmt.Errorf("repository lock metadata schema_version must equal %d", metadataSchemaVersion)
	}
	if metadata.PID <= 0 {
		return fmt.Errorf("repository lock metadata PID is invalid")
	}
	if !validMetadataText(metadata.Hostname, 255) {
		return fmt.Errorf("repository lock metadata hostname is invalid")
	}
	if !validMetadataText(metadata.Command, 128) {
		return fmt.Errorf("repository lock metadata command is invalid")
	}
	if metadata.StartedAt.IsZero() {
		return fmt.Errorf("repository lock metadata start time is invalid")
	}
	return nil
}

func validMetadataText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func decodeStrict(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("metadata contains multiple JSON values")
		}
		return err
	}
	return nil
}
