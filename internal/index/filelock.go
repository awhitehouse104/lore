package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

type fileLock struct {
	file *os.File
}

func acquireIndexLock(directory string, exclusive bool) (*fileLock, error) {
	path := filepath.Join(directory, "index.operation.lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open index operation lock: %w", err)
	}
	operation := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		operation = syscall.LOCK_EX | syscall.LOCK_NB
	}
	if err := syscall.Flock(int(file.Fd()), operation); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, newError(ErrorConflict, "index_busy", "another index operation is active", nil)
		}
		return nil, fmt.Errorf("acquire index operation lock: %w", err)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("release index operation lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close index operation lock: %w", closeErr)
	}
	return nil
}
