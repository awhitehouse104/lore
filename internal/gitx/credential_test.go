package gitx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNetworkRunnerAllowsStoredCredentialHelperButNeverAskpass(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root := t.TempDir()
	runCredentialTestGit(t, "", "init", "--initial-branch=main", root)
	helperMarker := filepath.Join(t.TempDir(), "helper-ran")
	askpassMarker := filepath.Join(t.TempDir(), "askpass-ran")
	helper := writeCredentialTestExecutable(t, "credential-helper", fmt.Sprintf(`#!/bin/sh
printf ran > %q
while IFS= read -r line; do
	[ -z "$line" ] && break
done
if [ "$1" = "get" ]; then
	printf 'username=lore\npassword=test-token\n'
fi
`, helperMarker))
	askpass := writeCredentialTestExecutable(
		t,
		"askpass",
		fmt.Sprintf("#!/bin/sh\nprintf ran > %q\nprintf stolen\n", askpassMarker),
	)
	runCredentialTestGit(t, root, "config", "--add", "credential.helper", "")
	runCredentialTestGit(t, root, "config", "--add", "credential.helper", helper)
	runCredentialTestGit(t, root, "config", "core.askPass", askpass)

	client := Client{
		Executable:     "git",
		NetworkTimeout: 3 * time.Second,
		Environment: []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + t.TempDir(),
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_ASKPASS=" + askpass,
			"DISPLAY=:99",
		},
	}
	output, err := client.runCommandWithInput(
		context.Background(),
		root,
		networkCommand,
		[]byte("protocol=https\nhost=example.invalid\n\n"),
		"credential",
		"fill",
	)
	if err != nil {
		t.Fatalf("credential fill: %v", err)
	}
	if !strings.Contains(string(output), "username=lore\n") ||
		!strings.Contains(string(output), "password=test-token\n") {
		t.Fatalf("credential output = %q", output)
	}
	if _, err := os.Stat(helperMarker); err != nil {
		t.Fatalf("credential helper did not run: %v", err)
	}
	if _, err := os.Stat(askpassMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("askpass program executed: %v", err)
	}

	runCredentialTestGit(t, root, "config", "--unset-all", "credential.helper")
	runCredentialTestGit(t, root, "config", "--add", "credential.helper", "")
	_, err = client.runCommandWithInput(
		context.Background(),
		root,
		networkCommand,
		[]byte("protocol=https\nhost=example.invalid\n\n"),
		"credential",
		"fill",
	)
	if err == nil {
		t.Fatal("credential fill without stored credentials unexpectedly succeeded")
	}
	if _, err := os.Stat(askpassMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("askpass program executed for missing credentials: %v", err)
	}
}

func runCredentialTestGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	commandArgs := []string{"-c", "core.hooksPath=/dev/null"}
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
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func writeCredentialTestExecutable(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
