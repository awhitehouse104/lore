package gitx_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"lore/internal/gitx"
)

func TestCommitAndPushDisableRepositoryExecutionHooks(t *testing.T) {
	root, client := hardeningRepository(t)
	hookMarker := filepath.Join(t.TempDir(), "hook-ran")
	gpgMarker := filepath.Join(t.TempDir(), "gpg-ran")
	hook := writeExecutable(t, "hook", fmt.Sprintf("#!/bin/sh\nprintf ran > %q\nexit 91\n", hookMarker))
	gpg := writeExecutable(t, "gpg", fmt.Sprintf("#!/bin/sh\nprintf ran > %q\nexit 92\n", gpgMarker))
	hooksDirectory := filepath.Join(root, ".git", "hooks")
	for _, name := range []string{
		"pre-commit",
		"prepare-commit-msg",
		"commit-msg",
		"post-commit",
		"reference-transaction",
		"post-index-change",
		"pre-push",
	} {
		data, err := os.ReadFile(hook)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hooksDirectory, name), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "config", "commit.gpgSign", "true")
	runGit(t, root, "config", "push.gpgSign", "true")
	runGit(t, root, "config", "gpg.program", gpg)
	runGit(t, root, "config", "core.fsmonitor", hook)

	notePath := filepath.Join(root, "note.md")
	if err := os.WriteFile(notePath, []byte("updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Changes(context.Background(), root, []string{"note.md"}); err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if _, err := client.CommitPath(context.Background(), root, "note.md", "test: hardened commit"); err != nil {
		t.Fatalf("CommitPath: %v", err)
	}

	bare := filepath.Join(t.TempDir(), "remote.git")
	runGit(t, "", "init", "--bare", "--initial-branch=main", bare)
	runGit(t, root, "remote", "add", "origin", bare)
	if err := client.PushHead(context.Background(), root, "origin"); err != nil {
		t.Fatalf("PushHead: %v", err)
	}
	if _, err := os.Stat(hookMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository hook or fsmonitor executed: %v", err)
	}
	if _, err := os.Stat(gpgMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configured signing program executed: %v", err)
	}
}

func TestRunnerSanitizesEnvironment(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "environment")
	executable := writeExecutable(t, "fake-git", fmt.Sprintf(
		"#!/bin/sh\n/usr/bin/env > %q\nexit 1\n",
		marker,
	))
	client := gitx.Client{
		Executable: executable,
		Environment: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=/private/service-home",
			"SSH_AUTH_SOCK=/run/private-agent.sock",
			"GIT_CONFIG_GLOBAL=/etc/lore/gitconfig",
			"GIT_ASKPASS=/tmp/malicious-askpass",
			"GIT_SSL_NO_VERIFY=1",
			"GIT_TRACE=/tmp/private-trace",
			"DISPLAY=:99",
			"UNRELATED_SECRET=must-not-reach-git",
		},
	}
	_, err := client.Head(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("fake Git unexpectedly succeeded")
	}
	environment := readEnvironment(t, marker)
	for key, expected := range map[string]string{
		"EDITOR":              "/bin/false",
		"GCM_INTERACTIVE":     "never",
		"GIT_ASKPASS":         "/bin/false",
		"GIT_CONFIG_GLOBAL":   "/etc/lore/gitconfig",
		"GIT_EDITOR":          "/bin/false",
		"GIT_OPTIONAL_LOCKS":  "0",
		"GIT_TERMINAL_PROMPT": "0",
		"HOME":                "/private/service-home",
		"LC_ALL":              "C",
		"SSH_ASKPASS":         "/bin/false",
		"SSH_ASKPASS_REQUIRE": "force",
		"SSH_AUTH_SOCK":       "/run/private-agent.sock",
	} {
		if environment[key] != expected {
			t.Fatalf("%s = %q, want %q; environment=%v", key, environment[key], expected, environment)
		}
	}
	for _, key := range []string{
		"DISPLAY",
		"GIT_SSL_NO_VERIFY",
		"GIT_TRACE",
		"UNRELATED_SECRET",
	} {
		if _, exists := environment[key]; exists {
			t.Fatalf("unsafe environment key %s reached Git", key)
		}
	}
}

func TestNewUsesDocumentedTimeoutDefaults(t *testing.T) {
	client := gitx.New()
	if client.LocalTimeout != gitx.DefaultLocalTimeout ||
		client.NetworkTimeout != gitx.DefaultNetworkTimeout {
		t.Fatalf(
			"timeouts = local %s network %s",
			client.LocalTimeout,
			client.NetworkTimeout,
		)
	}
}

func TestManagedFilterAttributesFailBeforeExecution(t *testing.T) {
	root, client := hardeningRepository(t)
	marker := filepath.Join(t.TempDir(), "filter-ran")
	filter := writeExecutable(t, "filter", fmt.Sprintf(
		"#!/bin/sh\nprintf ran > %q\ncat\n",
		marker,
	))
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.md filter=evil\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "--", ".gitattributes")
	runGit(t, root, "commit", "-m", "test: add attributes")
	runGit(t, root, "config", "filter.evil.clean", filter)
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("filtered update\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	operations := []struct {
		name string
		run  func() error
	}{
		{
			name: "status",
			run: func() error {
				_, err := client.Changes(context.Background(), root, []string{"note.md"})
				return err
			},
		},
		{
			name: "path staging",
			run: func() error {
				_, err := client.CommitPath(context.Background(), root, "note.md", "test: filtered")
				return err
			},
		},
		{
			name: "all staging",
			run: func() error {
				return client.AddAll(context.Background(), root)
			},
		},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			err := operation.run()
			var filterErr *gitx.FilterAttributeError
			if !errors.As(err, &filterErr) || filterErr.Path != "note.md" || filterErr.Value != "evil" {
				t.Fatalf("error = %T %v, want note.md FilterAttributeError", err, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("clean filter executed: %v", err)
			}
		})
	}
}

func TestLocalTimeoutKillsCompleteProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "processes")
	executable := blockingExecutable(t, marker, false)
	client := gitx.Client{
		Executable:   executable,
		LocalTimeout: 100 * time.Millisecond,
		Environment:  hardeningEnvironment(t),
	}
	_, err := client.Head(context.Background(), t.TempDir())
	var commandErr *gitx.CommandError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &commandErr) || !commandErr.TimedOut {
		t.Fatalf("Head error = %T %v, want timed-out CommandError", err, err)
	}
	assertRecordedProcessesExit(t, marker)
}

func TestEarlierCallerDeadlineWinsAndKillsCompleteProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "processes")
	executable := blockingExecutable(t, marker, false)
	client := gitx.Client{
		Executable:   executable,
		LocalTimeout: 5 * time.Second,
		Environment:  hardeningEnvironment(t),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := client.Head(ctx, t.TempDir())
	var commandErr *gitx.CommandError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &commandErr) || !commandErr.TimedOut {
		t.Fatalf("Head error = %T %v, want caller-deadline CommandError", err, err)
	}
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("caller deadline took %s; runner timeout should not replace it", elapsed)
	}
	assertRecordedProcessesExit(t, marker)
}

func TestCallerCancellationKillsCompleteProcessGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "processes")
	executable := blockingExecutable(t, marker, false)
	client := gitx.Client{
		Executable:   executable,
		LocalTimeout: 5 * time.Second,
		Environment:  hardeningEnvironment(t),
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.Head(ctx, t.TempDir())
		result <- err
	}()
	waitForPath(t, marker, 2*time.Second)
	cancel()
	select {
	case err := <-result:
		var commandErr *gitx.CommandError
		if !errors.Is(err, context.Canceled) || !errors.As(err, &commandErr) || !commandErr.Canceled {
			t.Fatalf("Head error = %T %v, want canceled CommandError", err, err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Git command did not stop after cancellation")
	}
	assertRecordedProcessesExit(t, marker)
}

func TestPushUsesNetworkTimeoutAndKillsChildren(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "processes")
	executable := blockingExecutable(t, marker, true)
	client := gitx.Client{
		Executable:     executable,
		LocalTimeout:   time.Second,
		NetworkTimeout: 100 * time.Millisecond,
		Environment:    hardeningEnvironment(t),
	}
	err := client.PushHead(context.Background(), t.TempDir(), "origin")
	var commandErr *gitx.CommandError
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &commandErr) ||
		!commandErr.TimedOut || commandErr.Command != "push" {
		t.Fatalf("PushHead error = %T %v, want timed-out push CommandError", err, err)
	}
	assertRecordedProcessesExit(t, marker)
}

func hardeningRepository(t *testing.T) (string, gitx.Client) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root := t.TempDir()
	runGit(t, "", "init", "--initial-branch=main", root)
	runGit(t, root, "config", "user.name", "Lore Test")
	runGit(t, root, "config", "user.email", "lore@example.invalid")
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "--", "note.md")
	runGit(t, root, "commit", "-m", "test: initial")
	return root, gitx.Client{
		Executable:     "git",
		LocalTimeout:   5 * time.Second,
		NetworkTimeout: 5 * time.Second,
		Environment:    hardeningEnvironment(t),
	}
}

func hardeningEnvironment(t *testing.T) []string {
	t.Helper()
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + t.TempDir(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	commandArgs := []string{"-c", "core.hooksPath=/dev/null", "-c", "commit.gpgSign=false"}
	if dir != "" {
		commandArgs = append(commandArgs, "-C", dir)
	}
	commandArgs = append(commandArgs, args...)
	command := exec.Command("git", commandArgs...)
	command.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}

func writeExecutable(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func blockingExecutable(t *testing.T, marker string, branchBeforeBlocking bool) string {
	t.Helper()
	fakeSSH := writeExecutable(t, "fake-ssh", "#!/bin/sh\nexec sleep 30\n")
	branchCase := ""
	if branchBeforeBlocking {
		branchCase = `for arg in "$@"; do
	if [ "$arg" = "symbolic-ref" ]; then
		printf 'main\n'
		exit 0
	fi
done
`
	}
	return writeExecutable(t, "blocking-git", fmt.Sprintf(`#!/bin/sh
%s
%q &
child=$!
printf '%%s %%s\n' "$$" "$child" > %q
wait "$child"
`, branchCase, fakeSSH, marker))
}

func assertRecordedProcessesExit(t *testing.T, marker string) {
	t.Helper()
	waitForPath(t, marker, 2*time.Second)
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	if len(fields) != 2 {
		t.Fatalf("process marker = %q", data)
	}
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatal(err)
		}
		waitForProcessExit(t, pid, 2*time.Second)
	}
}

func waitForProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if err != nil {
			t.Fatalf("inspect process %d: %v", pid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d survived Git cancellation", pid)
}

func waitForPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func readEnvironment(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	environment := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			environment[key] = value
		}
	}
	return environment
}
