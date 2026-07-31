package repository

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

var requiredRepositoryDirectories = []string{
	"pages",
	"sources",
	"assets",
	"system",
	".lore",
}

// ReadinessProbe verifies only the fixed repository substrate required by a
// running service. It deliberately does not parse configuration or documents,
// walk content roots, invoke Git, inspect the index, or acquire a write lock.
type ReadinessProbe struct {
	repo          *Repository
	root          fs.FileInfo
	configuration fs.FileInfo
	initialErr    error
}

func NewReadinessProbe(repo *Repository) *ReadinessProbe {
	probe := &ReadinessProbe{repo: repo}
	if repo == nil {
		probe.initialErr = errors.New("repository is not configured")
		return probe
	}

	root, err := inspectOpenDirectory(repo.Root)
	if err != nil {
		probe.initialErr = fmt.Errorf("inspect repository root: %w", err)
		return probe
	}
	configurationPath, err := repo.SafeRepositoryPath("lore.yaml")
	if err != nil {
		probe.initialErr = fmt.Errorf("resolve repository configuration: %w", err)
		return probe
	}
	configuration, err := inspectOpenRegular(configurationPath)
	if err != nil {
		probe.initialErr = fmt.Errorf("inspect repository configuration: %w", err)
		return probe
	}
	probe.root = root
	probe.configuration = configuration
	return probe
}

func (p *ReadinessProbe) Check() error {
	if p == nil {
		return errors.New("readiness probe is not configured")
	}
	if p.initialErr != nil {
		return p.initialErr
	}

	root, err := inspectOpenDirectory(p.repo.Root)
	if err != nil {
		return fmt.Errorf("inspect repository root: %w", err)
	}
	if !os.SameFile(p.root, root) {
		return errors.New("repository root changed after startup")
	}

	configurationPath, err := p.repo.SafeRepositoryPath("lore.yaml")
	if err != nil {
		return fmt.Errorf("resolve repository configuration: %w", err)
	}
	configuration, err := inspectOpenRegular(configurationPath)
	if err != nil {
		return fmt.Errorf("inspect repository configuration: %w", err)
	}
	if !os.SameFile(p.configuration, configuration) ||
		p.configuration.Size() != configuration.Size() ||
		!p.configuration.ModTime().Equal(configuration.ModTime()) {
		return errors.New("repository configuration changed after startup")
	}

	for _, relative := range requiredRepositoryDirectories {
		path, err := p.repo.SafeRepositoryPath(relative)
		if err != nil {
			return fmt.Errorf("resolve required repository directory %s: %w", relative, err)
		}
		if _, err := inspectOpenDirectory(path); err != nil {
			return fmt.Errorf("inspect required repository directory %s: %w", relative, err)
		}
	}

	activeRecovery, err := p.repo.SafeRepositoryPath(".lore/recovery/active")
	if err != nil {
		return fmt.Errorf("resolve active recovery journal: %w", err)
	}
	if _, err := os.Lstat(activeRecovery); err == nil {
		return errors.New("an active recovery journal blocks repository writes")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect active recovery journal: %w", err)
	}
	return nil
}

func inspectOpenDirectory(path string) (fs.FileInfo, error) {
	return inspectOpenPath(path, true)
}

func inspectOpenRegular(path string) (fs.FileInfo, error) {
	return inspectOpenPath(path, false)
}

func inspectOpenPath(path string, directory bool) (fs.FileInfo, error) {
	if path == "" {
		return nil, errors.New("path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("path is a symlink")
	}
	if directory && !info.IsDir() {
		return nil, errors.New("path is not a directory")
	}
	if !directory && !info.Mode().IsRegular() {
		return nil, errors.New("path is not a regular file")
	}

	opened, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer opened.Close()
	openedInfo, err := opened.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, errors.New("path changed while it was inspected")
	}
	if directory && !openedInfo.IsDir() {
		return nil, errors.New("opened path is not a directory")
	}
	if !directory && !openedInfo.Mode().IsRegular() {
		return nil, errors.New("opened path is not a regular file")
	}
	return openedInfo, nil
}
