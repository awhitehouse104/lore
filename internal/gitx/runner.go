package gitx

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultLocalTimeout   = 30 * time.Second
	DefaultNetworkTimeout = 2 * time.Minute
	commandWaitDelay      = time.Second
)

type commandClass uint8

const (
	localCommand commandClass = iota
	networkCommand
)

var hardenedConfiguration = []string{
	"core.hooksPath=/dev/null",
	"core.fsmonitor=false",
	"core.askPass=/bin/false",
	"credential.interactive=false",
	"commit.gpgSign=false",
	"push.gpgSign=false",
	"log.showSignature=false",
	"gc.auto=0",
	"maintenance.auto=false",
	"protocol.ext.allow=never",
	"core.autocrlf=false",
}

var inheritedEnvironmentKeys = map[string]struct{}{
	"ALL_PROXY":           {},
	"CURL_CA_BUNDLE":      {},
	"GIT_CONFIG_GLOBAL":   {},
	"GIT_CONFIG_NOSYSTEM": {},
	"GIT_CONFIG_SYSTEM":   {},
	"GIT_SSL_CAINFO":      {},
	"GIT_SSL_CAPATH":      {},
	"HOME":                {},
	"HTTPS_PROXY":         {},
	"HTTP_PROXY":          {},
	"LOGNAME":             {},
	"NO_PROXY":            {},
	"PATH":                {},
	"SHELL":               {},
	"SSH_AUTH_SOCK":       {},
	"SSL_CERT_DIR":        {},
	"SSL_CERT_FILE":       {},
	"TEMP":                {},
	"TMP":                 {},
	"TMPDIR":              {},
	"TZ":                  {},
	"USER":                {},
	"XDG_CONFIG_HOME":     {},
	"XDG_RUNTIME_DIR":     {},
	"all_proxy":           {},
	"http_proxy":          {},
	"https_proxy":         {},
	"no_proxy":            {},
}

var forcedEnvironment = map[string]string{
	"EDITOR":              "/bin/false",
	"GCM_INTERACTIVE":     "never",
	"GIT_ASKPASS":         "/bin/false",
	"GIT_EDITOR":          "/bin/false",
	"GIT_OPTIONAL_LOCKS":  "0",
	"GIT_PAGER":           "cat",
	"GIT_SEQUENCE_EDITOR": "/bin/false",
	"GIT_TERMINAL_PROMPT": "0",
	"LC_ALL":              "C",
	"PAGER":               "cat",
	"SSH_ASKPASS":         "/bin/false",
	"SSH_ASKPASS_REQUIRE": "force",
	"VISUAL":              "/bin/false",
}

func (c Client) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	stdout, err := c.runCommand(ctx, dir, localCommand, args...)
	if err != nil {
		return nil, err
	}
	return stdout, nil
}

func (c Client) runNetwork(ctx context.Context, dir string, args ...string) ([]byte, error) {
	stdout, err := c.runCommand(ctx, dir, networkCommand, args...)
	if err != nil {
		return nil, err
	}
	return stdout, nil
}

func (c Client) runCaptureOnExit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return c.runCommand(ctx, dir, localCommand, args...)
}

func (c Client) runCommand(
	ctx context.Context,
	dir string,
	class commandClass,
	args ...string,
) ([]byte, error) {
	return c.runCommandWithInput(ctx, dir, class, nil, args...)
}

func (c Client) runCommandWithInput(
	ctx context.Context,
	dir string,
	class commandClass,
	input []byte,
	args ...string,
) ([]byte, error) {
	if ctx == nil {
		return nil, &CommandError{
			Command:  commandName(args),
			ExitCode: -1,
			Cause:    errors.New("Git command context is required"),
		}
	}
	timeout := c.timeout(class)
	commandContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	executable := c.Executable
	if executable == "" {
		executable = "git"
	}
	commandArgs := make([]string, 0, len(hardenedConfiguration)*2+2+len(args))
	for _, value := range hardenedConfiguration {
		commandArgs = append(commandArgs, "-c", value)
	}
	commandArgs = append(commandArgs, "-C", dir)
	commandArgs = append(commandArgs, args...)
	command := exec.CommandContext(commandContext, executable, commandArgs...)
	command.Env = c.environment()
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.WaitDelay = commandWaitDelay
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	var stdout, stderr bytes.Buffer
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
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
	cause := err
	timedOut := false
	canceled := false
	if contextErr := commandContext.Err(); contextErr != nil {
		cause = contextErr
		timedOut = errors.Is(contextErr, context.DeadlineExceeded)
		canceled = errors.Is(contextErr, context.Canceled)
	}
	return stdout.Bytes(), &CommandError{
		Command:       commandName(args),
		ExitCode:      exitCode,
		NotRepository: strings.Contains(strings.ToLower(stderr.String()), "not a git repository"),
		TimedOut:      timedOut,
		Canceled:      canceled,
		Cause:         cause,
	}
}

func (c Client) timeout(class commandClass) time.Duration {
	if class == networkCommand {
		if c.NetworkTimeout > 0 {
			return c.NetworkTimeout
		}
		return DefaultNetworkTimeout
	}
	if c.LocalTimeout > 0 {
		return c.LocalTimeout
	}
	return DefaultLocalTimeout
}

func (c Client) environment() []string {
	base := c.Environment
	if base == nil {
		base = os.Environ()
	}
	values := make(map[string]string, len(inheritedEnvironmentKeys)+len(forcedEnvironment))
	for _, entry := range base {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, allowed := inheritedEnvironmentKeys[key]; allowed {
			values[key] = value
		}
	}
	for key, value := range forcedEnvironment {
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	environment := make([]string, 0, len(keys))
	for _, key := range keys {
		environment = append(environment, key+"="+values[key])
	}
	return environment
}

func commandName(args []string) string {
	if len(args) == 0 {
		return "command"
	}
	return args[0]
}
