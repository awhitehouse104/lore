package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CommandError struct {
	Command       string
	ExitCode      int
	NotRepository bool
	TimedOut      bool
	Canceled      bool
	Cause         error
}

func (e *CommandError) Error() string {
	if e.TimedOut {
		return fmt.Sprintf("git %s timed out", e.Command)
	}
	if e.Canceled {
		return fmt.Sprintf("git %s was canceled", e.Command)
	}
	return fmt.Sprintf("git %s failed (exit %d)", e.Command, e.ExitCode)
}

func (e *CommandError) Unwrap() error {
	return e.Cause
}

type Client struct {
	Executable     string
	LocalTimeout   time.Duration
	NetworkTimeout time.Duration
	Environment    []string
}

type FilterAttributeError struct {
	Path  string
	Value string
}

func (e *FilterAttributeError) Error() string {
	return fmt.Sprintf("Git filter attribute %q is not allowed for Lore-managed path %q", e.Value, e.Path)
}

type Change struct {
	Status string
	Path   string
}

type Commit struct {
	Hash        string    `json:"hash"`
	CommittedAt time.Time `json:"committed_at"`
	AuthorName  string    `json:"author_name"`
	AuthorEmail string    `json:"author_email"`
	Subject     string    `json:"subject"`
}

func New() Client {
	return Client{
		Executable:     "git",
		LocalTimeout:   DefaultLocalTimeout,
		NetworkTimeout: DefaultNetworkTimeout,
	}
}

func (c Client) IsRepository(ctx context.Context, dir string) (bool, error) {
	stdout, err := c.run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) && commandErr.NotRepository {
			return false, nil
		}
		return false, err
	}
	top := strings.TrimSpace(string(stdout))
	return top == dir, nil
}

func (c Client) Init(ctx context.Context, dir, branch string) error {
	_, err := c.run(ctx, dir, "init", "-b", branch)
	return err
}

func (c Client) IdentityConfigured(ctx context.Context, dir string) (bool, error) {
	name, nameErr := c.run(ctx, dir, "config", "--get", "user.name")
	email, emailErr := c.run(ctx, dir, "config", "--get", "user.email")
	if nameErr != nil && !configMissing(nameErr) {
		return false, nameErr
	}
	if emailErr != nil && !configMissing(emailErr) {
		return false, emailErr
	}
	if nameErr != nil || emailErr != nil {
		return false, nil
	}
	return strings.TrimSpace(string(name)) != "" && strings.TrimSpace(string(email)) != "", nil
}

func (c Client) AddAll(ctx context.Context, dir string) error {
	paths, err := c.stageablePaths(ctx, dir, nil)
	if err != nil {
		return err
	}
	if err := c.requireNoFilterAttributes(ctx, dir, paths); err != nil {
		return err
	}
	_, err = c.run(ctx, dir, "add", "--", ".")
	return err
}

func (c Client) CommitAll(ctx context.Context, dir, subject string) (string, error) {
	if _, err := c.run(ctx, dir, "commit", "--no-gpg-sign", "-m", subject); err != nil {
		return "", err
	}
	return c.Head(ctx, dir)
}

func (c Client) CommitPath(ctx context.Context, dir, path, subject string) (string, error) {
	return c.CommitPaths(ctx, dir, []string{path}, subject)
}

func (c Client) CommitPaths(ctx context.Context, dir string, paths []string, subject string) (string, error) {
	if err := c.requireNoFilterAttributes(ctx, dir, paths); err != nil {
		return "", err
	}
	addArgs := []string{"add", "--"}
	addArgs = append(addArgs, paths...)
	if _, err := c.run(ctx, dir, addArgs...); err != nil {
		return "", err
	}
	commitArgs := []string{"commit", "--no-gpg-sign", "--only", "-m", subject, "--"}
	commitArgs = append(commitArgs, paths...)
	if _, err := c.run(ctx, dir, commitArgs...); err != nil {
		return "", err
	}
	return c.Head(ctx, dir)
}

func (c Client) ResetPaths(ctx context.Context, dir string, paths []string) error {
	args := []string{"reset", "-q", "HEAD", "--"}
	args = append(args, paths...)
	_, err := c.run(ctx, dir, args...)
	return err
}

func (c Client) ChangedPathsInCommit(ctx context.Context, dir, commit string) ([]string, error) {
	stdout, err := c.run(ctx, dir, "diff-tree", "--root", "--no-commit-id", "--name-only", "-r", "-z", commit)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(stdout, []byte{0})
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 0 {
			paths = append(paths, filepathSlash(string(field)))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (c Client) BlobAtCommit(ctx context.Context, dir, commit, path string) ([]byte, error) {
	return c.run(ctx, dir, "show", commit+":"+path)
}

func (c Client) DirectChildren(ctx context.Context, dir, parent string) ([]string, error) {
	stdout, err := c.run(ctx, dir, "rev-list", "--all", "--parents")
	if err != nil {
		return nil, err
	}
	var children []string
	for _, line := range strings.Split(strings.TrimSpace(string(stdout)), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == parent {
			children = append(children, fields[0])
		}
	}
	sort.Strings(children)
	return children, nil
}

func (c Client) IsAncestor(ctx context.Context, dir, ancestor, descendant string) (bool, error) {
	_, err := c.run(ctx, dir, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

func (c Client) CommitReachable(ctx context.Context, dir, commit string) (bool, error) {
	stdout, err := c.run(
		ctx,
		dir,
		"for-each-ref",
		"--format=%(refname)",
		"--contains="+commit,
		"refs/heads",
		"refs/remotes",
		"refs/tags",
	)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(stdout)) > 0, nil
}

func (c Client) CommitTime(ctx context.Context, dir, commit string) (time.Time, error) {
	stdout, err := c.run(ctx, dir, "show", "-s", "--format=%cI", commit)
	if err != nil {
		return time.Time{}, err
	}
	value, err := time.Parse(time.RFC3339, strings.TrimSpace(string(stdout)))
	if err != nil {
		return time.Time{}, fmt.Errorf("parse Git commit time: %w", err)
	}
	return value.UTC(), nil
}

func (c Client) Head(ctx context.Context, dir string) (string, error) {
	stdout, err := c.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(stdout)), nil
}

func (c Client) HeadOptional(ctx context.Context, dir string) (string, bool, error) {
	stdout, err := c.run(ctx, dir, "rev-parse", "--verify", "HEAD")
	if err == nil {
		return strings.TrimSpace(string(stdout)), true, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) && (commandErr.ExitCode == 1 || commandErr.ExitCode == 128) {
		return "", false, nil
	}
	return "", false, err
}

func (c Client) CommonDirectory(ctx context.Context, dir string) (string, error) {
	stdout, err := c.run(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	path := strings.TrimSpace(string(stdout))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve Git common directory: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return filepath.Clean(path), nil
	}
	return "", fmt.Errorf("resolve Git common directory: %w", err)
}

func (c Client) RootCommits(ctx context.Context, dir string) ([]string, error) {
	stdout, err := c.run(ctx, dir, "rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		return nil, err
	}
	roots := strings.Fields(string(stdout))
	sort.Strings(roots)
	return roots, nil
}

func (c Client) CurrentBranch(ctx context.Context, dir string) (string, error) {
	branch, detached, err := c.BranchState(ctx, dir)
	if err != nil {
		return "", err
	}
	if detached {
		return "", fmt.Errorf("Git HEAD is detached")
	}
	return branch, nil
}

func (c Client) BranchState(ctx context.Context, dir string) (string, bool, error) {
	stdout, err := c.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
			return "", true, nil
		}
		return "", false, err
	}
	return strings.TrimSpace(string(stdout)), false, nil
}

func (c Client) SourceChanges(ctx context.Context, dir string) ([]Change, error) {
	return c.Changes(ctx, dir, []string{"sources"})
}

func (c Client) Changes(ctx context.Context, dir string, paths []string) ([]Change, error) {
	managedPaths, err := c.stageablePaths(ctx, dir, paths)
	if err != nil {
		return nil, err
	}
	if err := c.requireNoFilterAttributes(ctx, dir, managedPaths); err != nil {
		return nil, err
	}
	args := []string{"status", "--porcelain=v1", "-z", "--untracked-files=all", "--"}
	args = append(args, paths...)
	stdout, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(stdout, []byte{0})
	changes := make([]Change, 0, len(fields))
	for index := 0; index < len(fields); index++ {
		field := fields[index]
		if len(field) < 4 {
			continue
		}
		status := string(field[:2])
		path := string(field[3:])
		changes = append(changes, Change{Status: status, Path: filepathSlash(path)})
		if status[0] == 'R' || status[0] == 'C' || status[1] == 'R' || status[1] == 'C' {
			index++
		}
	}
	return changes, nil
}

// NoIndexDiff returns the unified diff between two filesystem paths. Git uses
// exit status 1 to report that the paths differ; that is a successful result.
func (c Client) NoIndexDiff(ctx context.Context, dir, oldPath, newPath string) ([]byte, error) {
	args := []string{
		"diff", "--no-index", "--no-color", "--no-ext-diff", "--no-textconv",
		"--", oldPath, newPath,
	}
	stdout, err := c.runCaptureOnExit(ctx, dir, args...)
	if err == nil {
		return stdout, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
		return stdout, nil
	}
	return nil, err
}

func (c Client) Recent(ctx context.Context, dir string, limit int, contentOnly bool) ([]Commit, error) {
	head, err := c.run(ctx, dir, "rev-list", "--all", "--max-count=1")
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(head)) == 0 {
		return []Commit{}, nil
	}
	args := []string{
		"log", "-z", "-n", strconv.Itoa(limit),
		"--format=%H%x00%cI%x00%an%x00%ae%x00%s",
	}
	if contentOnly {
		args = append(args, "--", "pages", "sources")
	}
	stdout, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(stdout, []byte{0})
	for len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
		fields = fields[:len(fields)-1]
	}
	if len(fields)%5 != 0 {
		return nil, fmt.Errorf("parse git log: expected groups of 5 fields, got %d fields", len(fields))
	}
	commits := make([]Commit, 0, len(fields)/5)
	for index := 0; index < len(fields); index += 5 {
		committedAt, parseErr := time.Parse(time.RFC3339, string(fields[index+1]))
		if parseErr != nil {
			return nil, fmt.Errorf("parse git commit timestamp: %w", parseErr)
		}
		commits = append(commits, Commit{
			Hash:        string(fields[index]),
			CommittedAt: committedAt.UTC(),
			AuthorName:  string(fields[index+2]),
			AuthorEmail: string(fields[index+3]),
			Subject:     string(fields[index+4]),
		})
	}
	return commits, nil
}

func (c Client) IsIgnored(ctx context.Context, dir, path string) (bool, error) {
	_, err := c.run(ctx, dir, "check-ignore", "-q", "--", path)
	if err == nil {
		return true, nil
	}
	var commandErr *CommandError
	if errors.As(err, &commandErr) && commandErr.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

func (c Client) TrackedPaths(ctx context.Context, dir string, patterns []string) ([]string, error) {
	args := []string{"ls-files", "-z", "--"}
	args = append(args, patterns...)
	stdout, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(stdout, []byte{0})
	paths := make([]string, 0, len(fields))
	for _, field := range fields {
		if len(field) > 0 {
			paths = append(paths, filepathSlash(string(field)))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func (c Client) PushHead(ctx context.Context, dir, remote string) error {
	branch, err := c.CurrentBranch(ctx, dir)
	if err != nil {
		return err
	}
	_, err = c.runNetwork(ctx, dir, "push", "--no-signed", remote, "HEAD:refs/heads/"+branch)
	return err
}

func (c Client) stageablePaths(ctx context.Context, dir string, pathspecs []string) ([]string, error) {
	args := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	if len(pathspecs) > 0 {
		args = append(args, "--")
		args = append(args, pathspecs...)
	}
	stdout, err := c.run(ctx, dir, args...)
	if err != nil {
		return nil, err
	}
	fields := bytes.Split(stdout, []byte{0})
	paths := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if len(field) == 0 {
			continue
		}
		path := filepathSlash(string(field))
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

func (c Client) requireNoFilterAttributes(ctx context.Context, dir string, paths []string) error {
	const batchSize = 256
	for start := 0; start < len(paths); start += batchSize {
		end := min(start+batchSize, len(paths))
		args := []string{"check-attr", "-z", "filter", "--"}
		args = append(args, paths[start:end]...)
		stdout, err := c.run(ctx, dir, args...)
		if err != nil {
			return err
		}
		fields := bytes.Split(stdout, []byte{0})
		for len(fields) > 0 && len(fields[len(fields)-1]) == 0 {
			fields = fields[:len(fields)-1]
		}
		if len(fields)%3 != 0 {
			return fmt.Errorf("parse git check-attr: expected groups of 3 fields, got %d fields", len(fields))
		}
		for index := 0; index < len(fields); index += 3 {
			if string(fields[index+1]) != "filter" {
				return fmt.Errorf("parse git check-attr: unexpected attribute %q", fields[index+1])
			}
			value := string(fields[index+2])
			if value == "unspecified" || value == "unset" {
				continue
			}
			return &FilterAttributeError{
				Path:  filepathSlash(string(fields[index])),
				Value: value,
			}
		}
	}
	return nil
}

func configMissing(err error) bool {
	var commandErr *CommandError
	return errors.As(err, &commandErr) && commandErr.ExitCode == 1
}

func filepathSlash(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}
