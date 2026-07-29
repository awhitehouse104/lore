package idempotency

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"lore/internal/repository"
)

const (
	SchemaVersion    = 1
	DefaultTTL       = 7 * 24 * time.Hour
	MaximumKeyBytes  = 256
	maxRecordBytes   = 1 * 1024 * 1024
	maxCleanupFiles  = 2000
	maxStoredRecords = 4096
)

var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Store struct {
	root  string
	clock Clock
	ttl   time.Duration
}

type Record struct {
	SchemaVersion int             `json:"schema_version"`
	Principal     string          `json:"principal"`
	Operation     string          `json:"operation"`
	KeyHash       string          `json:"key_hash"`
	InputDigest   string          `json:"input_digest"`
	Result        json.RawMessage `json:"result"`
	CreatedAt     string          `json:"created_at"`
	ExpiresAt     string          `json:"expires_at"`
}

type Lease struct {
	store      *Store
	lockFile   *os.File
	recordPath string
	record     Record
	closed     bool
}

type ConflictError struct{}

func (*ConflictError) Error() string {
	return "idempotency key was already used with different input"
}

type LockedError struct{}

func (*LockedError) Error() string {
	return "idempotency key is currently being processed"
}

func NewStore(repo *repository.Repository, clock Clock, ttl time.Duration) (*Store, error) {
	if repo == nil {
		return nil, fmt.Errorf("repository is required")
	}
	root, err := repo.SafeRepositoryPath(".lore/idempotency")
	if err != nil {
		return nil, fmt.Errorf("resolve idempotency store: %w", err)
	}
	if clock == nil {
		clock = realClock{}
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Store{root: root, clock: clock, ttl: ttl}, nil
}

func DigestInput(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("marshal canonical idempotency input: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s *Store) Begin(principal, operation, key, inputDigest string) (*Lease, json.RawMessage, bool, error) {
	if err := validateIdentity(principal, operation, key, inputDigest); err != nil {
		return nil, nil, false, err
	}
	if err := s.ensureRoot(); err != nil {
		return nil, nil, false, err
	}
	guard, err := s.lockStore()
	if err != nil {
		return nil, nil, false, err
	}
	guardHeld := true
	releaseGuard := func() {
		if guardHeld {
			_ = unlockAndClose(guard)
			guardHeld = false
		}
	}
	defer releaseGuard()
	_ = s.cleanupLocked()
	hash := keyHash(principal, operation, key)
	recordPath := filepath.Join(s.root, hash+".json")
	record, found, err := s.load(recordPath, principal, operation, hash, inputDigest)
	if err != nil || found {
		if found {
			return nil, append(json.RawMessage(nil), record.Result...), true, nil
		}
		return nil, nil, false, err
	}
	if count, countErr := s.recordCountLocked(); countErr != nil {
		return nil, nil, false, countErr
	} else if count >= maxStoredRecords {
		return nil, nil, false, fmt.Errorf("idempotency store has reached its bounded capacity")
	}
	lockPath := filepath.Join(s.root, hash+".lock")
	if info, err := os.Lstat(lockPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, nil, false, fmt.Errorf("idempotency lease path is invalid")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, false, fmt.Errorf("inspect idempotency lease: %w", err)
	}
	file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, nil, false, fmt.Errorf("create idempotency lease: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, nil, false, fmt.Errorf("set idempotency lease permissions: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, nil, false, &LockedError{}
		}
		return nil, nil, false, fmt.Errorf("lock idempotency lease: %w", err)
	}
	record, found, err = s.load(recordPath, principal, operation, hash, inputDigest)
	if err != nil || found {
		_ = unlockAndClose(file)
		if found {
			return nil, append(json.RawMessage(nil), record.Result...), true, nil
		}
		return nil, nil, false, err
	}
	now := s.clock.Now().UTC()
	releaseGuard()
	return &Lease{
		store:      s,
		lockFile:   file,
		recordPath: recordPath,
		record: Record{
			SchemaVersion: SchemaVersion,
			Principal:     principal,
			Operation:     operation,
			KeyHash:       hash,
			InputDigest:   inputDigest,
			CreatedAt:     now.Format(time.RFC3339Nano),
			ExpiresAt:     now.Add(s.ttl).Format(time.RFC3339Nano),
		},
	}, nil, false, nil
}

func (l *Lease) Commit(result any) error {
	if l == nil || l.closed {
		return fmt.Errorf("idempotency lease is closed")
	}
	resultBytes, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("marshal idempotency result: %w", err)
	}
	if len(resultBytes) > maxRecordBytes/2 {
		return fmt.Errorf("idempotency result exceeds the supported size")
	}
	l.record.Result = resultBytes
	data, err := json.Marshal(l.record)
	if err != nil {
		return fmt.Errorf("marshal idempotency record: %w", err)
	}
	data = append(data, '\n')
	if len(data) > maxRecordBytes {
		return fmt.Errorf("idempotency record exceeds the supported size")
	}
	temp, err := os.CreateTemp(l.store.root, ".tmp-")
	if err != nil {
		return fmt.Errorf("create idempotency temporary file: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("set idempotency temporary permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write idempotency record: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush idempotency record: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close idempotency record: %w", err)
	}
	if err := os.Link(tempPath, l.recordPath); err != nil {
		_ = os.Remove(tempPath)
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("idempotency record already exists")
		}
		return fmt.Errorf("publish idempotency record: %w", err)
	}
	if err := os.Remove(tempPath); err != nil {
		return fmt.Errorf("remove idempotency temporary link: %w", err)
	}
	return syncDirectory(l.store.root)
}

func (l *Lease) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	unlockErr := syscall.Flock(int(l.lockFile.Fd()), syscall.LOCK_UN)
	closeErr := l.lockFile.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock idempotency lease: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close idempotency lease: %w", closeErr)
	}
	return nil
}

func (s *Store) load(path, principal, operation, hash, inputDigest string) (Record, bool, error) {
	pathInfo, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("inspect idempotency record: %w", err)
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Size() > maxRecordBytes {
		return Record{}, false, fmt.Errorf("idempotency record is invalid")
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return Record{}, false, fmt.Errorf("open idempotency record: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Record{}, false, fmt.Errorf("inspect idempotency record: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxRecordBytes {
		return Record{}, false, fmt.Errorf("idempotency record is invalid")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxRecordBytes+1))
	if err != nil {
		return Record{}, false, fmt.Errorf("read idempotency record: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, false, fmt.Errorf("decode idempotency record: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Record{}, false, fmt.Errorf("idempotency record contains trailing data")
	}
	if record.SchemaVersion != SchemaVersion ||
		record.Principal != principal ||
		record.Operation != operation ||
		record.KeyHash != hash ||
		!digestPattern.MatchString(record.InputDigest) ||
		len(record.Result) == 0 {
		return Record{}, false, fmt.Errorf("idempotency record failed validation")
	}
	expires, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
	if err != nil {
		return Record{}, false, fmt.Errorf("idempotency record expiry is invalid")
	}
	if !s.clock.Now().UTC().Before(expires) {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return Record{}, false, fmt.Errorf("remove expired idempotency record: %w", err)
		}
		return Record{}, false, nil
	}
	if record.InputDigest != inputDigest {
		return Record{}, false, &ConflictError{}
	}
	return record, true, nil
}

func (s *Store) ensureRoot() error {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create idempotency store: %w", err)
	}
	info, err := os.Lstat(s.root)
	if err != nil {
		return fmt.Errorf("inspect idempotency store: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("idempotency store is not a regular directory")
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return fmt.Errorf("set idempotency store permissions: %w", err)
	}
	return nil
}

func (s *Store) lockStore() (*os.File, error) {
	path := filepath.Join(s.root, ".store.lock")
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("idempotency store lock is invalid")
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect idempotency store lock: %w", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create idempotency store lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("set idempotency store lock permissions: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock idempotency store: %w", err)
	}
	return file, nil
}

func unlockAndClose(file *os.File) error {
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func (s *Store) cleanupLocked() error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	cleaned := 0
	for _, entry := range entries {
		if cleaned >= maxCleanupFiles {
			break
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		path := filepath.Join(s.root, name)
		switch {
		case strings.HasSuffix(name, ".json"):
			cleaned++
			file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				continue
			}
			var header struct {
				ExpiresAt string `json:"expires_at"`
			}
			decodeErr := json.NewDecoder(io.LimitReader(file, maxRecordBytes+1)).Decode(&header)
			_ = file.Close()
			expires, parseErr := time.Parse(time.RFC3339Nano, header.ExpiresAt)
			if decodeErr == nil && parseErr == nil && !s.clock.Now().UTC().Before(expires) {
				_ = os.Remove(path)
			}
		case strings.HasSuffix(name, ".lock") && name != ".store.lock":
			recordPath := strings.TrimSuffix(path, ".lock") + ".json"
			if _, err := os.Lstat(recordPath); err == nil || !errors.Is(err, fs.ErrNotExist) {
				continue
			}
			cleaned++
			file, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				continue
			}
			if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				_ = file.Close()
				continue
			}
			_ = os.Remove(path)
			_ = unlockAndClose(file)
		default:
			continue
		}
	}
	return nil
}

func (s *Store) recordCountLocked() (int, error) {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return 0, fmt.Errorf("count idempotency records: %w", err)
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
	}
	return count, nil
}

func validateIdentity(principal, operation, key, inputDigest string) error {
	if principal == "" || operation == "" {
		return fmt.Errorf("idempotency principal and operation are required")
	}
	if key == "" || len(key) > MaximumKeyBytes || !utf8.ValidString(key) {
		return fmt.Errorf("idempotency key must contain between 1 and %d valid UTF-8 bytes", MaximumKeyBytes)
	}
	for _, value := range []byte(key) {
		if value < 0x20 || value == 0x7f {
			return fmt.Errorf("idempotency key must not contain ASCII control characters")
		}
	}
	if !digestPattern.MatchString(inputDigest) {
		return fmt.Errorf("idempotency input digest is invalid")
	}
	return nil
}

func keyHash(principal, operation, key string) string {
	sum := sha256.Sum256([]byte(principal + "\x00" + operation + "\x00" + key))
	return hex.EncodeToString(sum[:])
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
