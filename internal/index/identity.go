package index

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"lore/internal/repository"
)

const detachedBranch = "detached"

var repositoryIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type snapshot struct {
	identity string
	isGit    bool
	head     string
	branch   string
}

func (m *Manager) currentSnapshot(ctx context.Context, createID bool) (snapshot, error) {
	if m == nil || m.Repo == nil || m.Git == nil {
		return snapshot{}, fmt.Errorf("index manager is not fully configured")
	}
	isGit, err := m.Git.IsRepository(ctx, m.Repo.Root)
	if err != nil {
		return snapshot{}, fmt.Errorf("inspect Git repository: %w", err)
	}
	if isGit {
		head, exists, err := m.Git.HeadOptional(ctx, m.Repo.Root)
		if err != nil {
			return snapshot{}, fmt.Errorf("inspect Git HEAD: %w", err)
		}
		branch, detached, err := m.Git.BranchState(ctx, m.Repo.Root)
		if err != nil {
			return snapshot{}, fmt.Errorf("inspect Git branch: %w", err)
		}
		if detached {
			branch = detachedBranch
		}
		if exists {
			commonDirectory, err := m.Git.CommonDirectory(ctx, m.Repo.Root)
			if err != nil {
				return snapshot{}, fmt.Errorf("inspect Git common directory: %w", err)
			}
			roots, err := m.Git.RootCommits(ctx, m.Repo.Root)
			if err != nil {
				return snapshot{}, fmt.Errorf("inspect Git root commit: %w", err)
			}
			if len(roots) == 0 {
				return snapshot{}, fmt.Errorf("Git HEAD exists without a root commit")
			}
			parts := append([]string{"git", filepath.Clean(commonDirectory)}, roots...)
			return snapshot{
				identity: digestIdentity(parts...),
				isGit:    true,
				head:     head,
				branch:   branch,
			}, nil
		}
		identity, err := repositoryUUID(m.Repo, createID)
		if err != nil {
			return snapshot{}, err
		}
		return snapshot{identity: "uuid:" + identity, isGit: true, branch: branch}, nil
	}
	identity, err := repositoryUUID(m.Repo, createID)
	if err != nil {
		return snapshot{}, err
	}
	return snapshot{identity: "uuid:" + identity, isGit: false}, nil
}

func digestIdentity(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}

func repositoryUUID(repo *repository.Repository, create bool) (string, error) {
	path, err := repo.SafeRepositoryPath(".lore/repository-id")
	if err != nil {
		return "", fmt.Errorf("resolve repository identity: %w", err)
	}
	data, err := os.ReadFile(path)
	if err == nil {
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return "", fmt.Errorf("inspect repository identity: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("repository identity must be a regular non-symlink file")
		}
		value := strings.TrimSpace(string(data))
		if !repositoryIDPattern.MatchString(value) {
			return "", fmt.Errorf("repository identity is malformed")
		}
		return value, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read repository identity: %w", err)
	}
	if !create {
		return "", fs.ErrNotExist
	}
	value, err := newUUID()
	if err != nil {
		return "", err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return repositoryUUID(repo, false)
		}
		return "", fmt.Errorf("create repository identity: %w", err)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.WriteString(value + "\n"); err != nil {
		return "", fmt.Errorf("write repository identity: %w", err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("flush repository identity: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close repository identity: %w", err)
	}
	remove = false
	syncDirectory(filepath.Dir(path))
	return value, nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate repository identity: %w", err)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
