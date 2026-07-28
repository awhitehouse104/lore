package repository

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// AtomicApply publishes exact bytes after verifying the expected destination
// state. expected is ignored when expectedExists is false.
func (r *Repository) AtomicApply(relative string, data, expected []byte, expectedExists bool) error {
	target, err := r.SafeContentPath(relative)
	if err != nil {
		return err
	}
	parent := filepath.Dir(target)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect content directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("content parent is not a regular directory")
	}
	if err := verifyExpectedFile(target, expected, expectedExists); err != nil {
		return err
	}
	file, err := os.CreateTemp(parent, ".lore-apply-*")
	if err != nil {
		return fmt.Errorf("create apply temporary file: %w", err)
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if err := file.Chmod(0o644); err != nil {
		cleanup()
		return fmt.Errorf("set apply file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("write apply temporary file: %w", err)
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("flush apply temporary file: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close apply temporary file: %w", err)
	}
	if err := verifyExpectedFile(target, expected, expectedExists); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if expectedExists {
		if err := os.Rename(tempPath, target); err != nil {
			_ = os.Remove(tempPath)
			return fmt.Errorf("publish replacement file: %w", err)
		}
	} else {
		if err := os.Link(tempPath, target); err != nil {
			_ = os.Remove(tempPath)
			if errors.Is(err, fs.ErrExist) {
				return &PathExistsError{Path: relative}
			}
			return fmt.Errorf("publish created file: %w", err)
		}
		if err := os.Remove(tempPath); err != nil {
			return fmt.Errorf("remove apply temporary link: %w", err)
		}
	}
	if directory, err := os.Open(parent); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	published, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("verify published file: %w", err)
	}
	if !bytes.Equal(published, data) {
		return fmt.Errorf("published file bytes do not match requested bytes")
	}
	return nil
}

func (r *Repository) RemoveExpected(relative string, expected []byte) error {
	target, err := r.SafeContentPath(relative)
	if err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return fmt.Errorf("inspect removal destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("removal destination is not a regular non-symlink file")
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return fmt.Errorf("read removal destination: %w", err)
	}
	if !bytes.Equal(current, expected) {
		return fmt.Errorf("removal destination no longer matches the expected bytes")
	}
	if err := os.Remove(target); err != nil {
		return fmt.Errorf("remove destination: %w", err)
	}
	if directory, err := os.Open(filepath.Dir(target)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func verifyExpectedFile(path string, expected []byte, expectedExists bool) error {
	info, err := os.Lstat(path)
	if !expectedExists {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect create destination: %w", err)
		}
		return fmt.Errorf("create destination unexpectedly exists")
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("replacement destination unexpectedly does not exist")
		}
		return fmt.Errorf("inspect replacement destination: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("replacement destination is not a regular non-symlink file")
	}
	current, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read replacement destination: %w", err)
	}
	if !bytes.Equal(current, expected) {
		return fmt.Errorf("replacement destination no longer matches the expected bytes")
	}
	return nil
}
