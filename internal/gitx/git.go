package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

type CommandError struct {
	Command  string
	ExitCode int
	Stderr   string
	Cause    error
}

func (e *CommandError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("git %s failed (exit %d): %s", e.Command, e.ExitCode, e.Stderr)
	}
	return fmt.Sprintf("git %s failed (exit %d)", e.Command, e.ExitCode)
}

func (e *CommandError) Unwrap() error {
	return e.Cause
}

type Client struct {
	Executable string
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
	return Client{Executable: "git"}
}

func (c Client) IsRepository(ctx context.Context, dir string) (bool, error) {
	stdout, err := c.run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		var commandErr *CommandError
		if errors.As(err, &commandErr) && strings.Contains(strings.ToLower(commandErr.Stderr), "not a git repository") {
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
	if nameErr != nil || emailErr != nil {
		if configMissing(nameErr) || configMissing(emailErr) {
			return false, nil
		}
		if nameErr != nil {
			return false, nameErr
		}
		return false, emailErr
	}
	return strings.TrimSpace(string(name)) != "" && strings.TrimSpace(string(email)) != "", nil
}

func (c Client) AddAll(ctx context.Context, dir string) error {
	_, err := c.run(ctx, dir, "add", "--", ".")
	return err
}

func (c Client) CommitAll(ctx context.Context, dir, subject string) (string, error) {
	if _, err := c.run(ctx, dir, "commit", "-m", subject); err != nil {
		return "", err
	}
	return c.Head(ctx, dir)
}

func (c Client) CommitPath(ctx context.Context, dir, path, subject string) (string, error) {
	if _, err := c.run(ctx, dir, "add", "--", path); err != nil {
		return "", err
	}
	if _, err := c.run(ctx, dir, "commit", "--only", "-m", subject, "--", path); err != nil {
		return "", err
	}
	return c.Head(ctx, dir)
}

func (c Client) Head(ctx context.Context, dir string) (string, error) {
	stdout, err := c.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(stdout)), nil
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
	stdout, err := c.run(ctx, dir, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--", "sources")
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

func (c Client) PushHead(ctx context.Context, dir, remote string) error {
	branch, err := c.CurrentBranch(ctx, dir)
	if err != nil {
		return err
	}
	_, err = c.run(ctx, dir, "push", remote, "HEAD:refs/heads/"+branch)
	return err
}

func (c Client) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	executable := c.Executable
	if executable == "" {
		executable = "git"
	}
	commandArgs := append([]string{"-C", dir}, args...)
	command := exec.CommandContext(ctx, executable, commandArgs...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	exitCode := -1
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		exitCode = exitErr.ExitCode()
	}
	name := "command"
	if len(args) > 0 {
		name = args[0]
	}
	return nil, &CommandError{
		Command:  name,
		ExitCode: exitCode,
		Stderr:   sanitize(stderr.String()),
		Cause:    err,
	}
}

func configMissing(err error) bool {
	var commandErr *CommandError
	return errors.As(err, &commandErr) && commandErr.ExitCode == 1
}

func sanitize(value string) string {
	value = strings.TrimSpace(value)
	const max = 2048
	if len(value) > max {
		value = value[:max] + "... (" + strconv.Itoa(len(value)-max) + " bytes omitted)"
	}
	return value
}

func filepathSlash(value string) string {
	return strings.ReplaceAll(value, "\\", "/")
}
