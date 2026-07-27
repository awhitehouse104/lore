package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"lore/internal/core"
	"lore/internal/gitx"
	"lore/internal/initrepo"
	"lore/internal/lint"
	"lore/internal/output"
	"lore/internal/repository"
	"lore/internal/version"
)

type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

type globalOptions struct {
	repo string
	json bool
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	s := streams{in: stdin, out: stdout, err: stderr}
	global, remaining, parseErr := parseGlobals(args)
	if parseErr != nil {
		return emitError(s, global.json, parseErr)
	}
	if len(remaining) == 0 {
		printRootUsage(stderr)
		return core.ExitUsage
	}

	switch remaining[0] {
	case "init":
		return runInit(ctx, remaining[1:], global, s)
	case "lint":
		return runLint(ctx, remaining[1:], global, s)
	case "version":
		return runVersion(remaining[1:], global, s)
	case "help", "--help", "-h":
		printRootUsage(stdout)
		return core.ExitOK
	default:
		return emitError(s, global.json, core.NewError(core.ExitUsage, "unknown_command", fmt.Sprintf("unknown command %q", remaining[0])))
	}
}

func parseGlobals(args []string) (globalOptions, []string, *core.APIError) {
	var options globalOptions
	index := 0
	for index < len(args) {
		arg := args[index]
		switch {
		case arg == "--json":
			options.json = true
			index++
		case arg == "--repo":
			if index+1 >= len(args) {
				return options, nil, core.NewError(core.ExitUsage, "missing_flag_value", "--repo requires a path")
			}
			options.repo = args[index+1]
			index += 2
		case strings.HasPrefix(arg, "--repo="):
			options.repo = strings.TrimPrefix(arg, "--repo=")
			if options.repo == "" {
				return options, nil, core.NewError(core.ExitUsage, "missing_flag_value", "--repo requires a path")
			}
			index++
		default:
			return options, args[index:], nil
		}
	}
	return options, nil, nil
}

func runInit(ctx context.Context, args []string, global globalOptions, s streams) int {
	jsonOutput := global.json
	noGit := false
	var path string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--json":
			jsonOutput = true
		case "--no-git":
			noGit = true
		case "--help", "-h":
			fmt.Fprintln(s.out, "Usage: lore init [PATH] [--no-git] [--json]")
			return core.ExitOK
		default:
			if strings.HasPrefix(args[index], "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore init: unknown flag %q", args[index])))
			}
			if path != "" {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore init accepts at most one path"))
			}
			path = args[index]
		}
	}
	if global.repo != "" {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "invalid_flag", "lore init uses its positional PATH and does not accept --repo"))
	}
	result, err := initrepo.Initialize(ctx, initrepo.Options{Path: path, NoGit: noGit}, gitx.New())
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write init output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "Initialized Lore repository at %s\n", result.Repo)
	fmt.Fprintf(s.out, "Created %d file(s); %d already existed.\n", len(result.CreatedFiles), len(result.ExistingFiles))
	if result.GitInitialized {
		fmt.Fprintln(s.out, "Initialized Git repository on branch main.")
	}
	if result.InitialCommit != "" {
		fmt.Fprintf(s.out, "Created initial commit %s.\n", shortHash(result.InitialCommit))
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(s.err, "warning: %s\n", warning)
	}
	return core.ExitOK
}

func runLint(ctx context.Context, args []string, global globalOptions, s streams) int {
	fs := flag.NewFlagSet("lint", flag.ContinueOnError)
	fs.SetOutput(s.err)
	repoPath := global.repo
	jsonOutput := global.json
	fs.StringVar(&repoPath, "repo", repoPath, "target Lore repository")
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: lore [--repo PATH] lint [--json]")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return core.ExitOK
		}
		return core.ExitUsage
	}
	if fs.NArg() != 0 {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore lint does not accept positional arguments"))
	}
	repo, apiErr := openRepository(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	result, err := lint.Run(ctx, repo, gitx.New())
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write lint output: %w", err))
		}
	} else {
		for _, finding := range result.Findings {
			location := finding.Path
			if finding.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, finding.Line)
			}
			fmt.Fprintf(s.out, "%s: %s: %s [%s]\n", finding.Severity, location, finding.Message, finding.Code)
		}
		fmt.Fprintf(s.out, "Lore lint: %d error(s), %d warning(s)\n", result.Errors, result.Warnings)
	}
	if result.Errors > 0 {
		return core.ExitValidation
	}
	return core.ExitOK
}

func runVersion(args []string, global globalOptions, s streams) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(s.err)
	jsonOutput := global.json
	fs.BoolVar(&jsonOutput, "json", jsonOutput, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: lore version [--json]")
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return core.ExitOK
		}
		return core.ExitUsage
	}
	if fs.NArg() != 0 {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore version does not accept positional arguments"))
	}

	info := version.Current()
	if jsonOutput {
		if err := output.JSON(s.out, info); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write version output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "lore %s (%s, %s)\n", info.Version, info.Commit, info.BuildDate)
	return core.ExitOK
}

func openRepository(explicit string) (*repository.Repository, *core.APIError) {
	cwd, err := os.Getwd()
	if err != nil {
		apiErr := core.NewError(core.ExitRuntime, "filesystem_error", "could not determine the current directory")
		apiErr.Cause = err
		return nil, apiErr
	}
	root, err := repository.Resolve(explicit, cwd, os.Getenv)
	if err != nil {
		apiErr := core.NewError(core.ExitUsage, "repository_not_found", err.Error())
		apiErr.Cause = err
		return nil, apiErr
	}
	repo, err := repository.Open(root)
	if err != nil {
		apiErr := core.NewError(core.ExitUsage, "invalid_configuration", err.Error())
		apiErr.Cause = err
		return nil, apiErr
	}
	return repo, nil
}

func emitOperationError(s streams, jsonOutput bool, err error) int {
	var apiErr *core.APIError
	if errors.As(err, &apiErr) {
		return emitError(s, jsonOutput, apiErr)
	}
	apiErr = core.NewError(core.ExitRuntime, "operation_failed", err.Error())
	apiErr.Cause = err
	return emitError(s, jsonOutput, apiErr)
}

func emitError(s streams, jsonOutput bool, apiErr *core.APIError) int {
	if apiErr.Details == nil {
		apiErr.Details = map[string]any{}
	}
	if jsonOutput {
		envelope := core.ErrorEnvelope{SchemaVersion: core.SchemaVersion, Error: apiErr}
		if err := json.NewEncoder(s.out).Encode(envelope); err != nil {
			fmt.Fprintf(s.err, "lore: write JSON error: %v\n", err)
		}
	} else {
		fmt.Fprintf(s.err, "lore: %s\n", apiErr.Message)
	}
	return apiErr.ExitCode
}

func shortHash(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, `Lore is a deterministic Markdown-and-Git knowledge repository CLI.

Usage:
  lore [--repo PATH] [--json] <command> [options]

Commands:
  init      initialize a knowledge repository
  capture   preserve raw source material
  search    find lexical evidence
  read      read a managed document
  lint      validate repository integrity
  recent    inspect recent knowledge commits
  version   print build version

Run "lore <command> --help" for command-specific help.`)
}
