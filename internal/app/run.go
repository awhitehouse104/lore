package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"lore/internal/core"
	"lore/internal/gitx"
	"lore/internal/initrepo"
	captureinput "lore/internal/input"
	"lore/internal/lint"
	"lore/internal/output"
	"lore/internal/repository"
	"lore/internal/search"
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
	case "capture":
		return runCapture(ctx, remaining[1:], global, s)
	case "read":
		return runRead(ctx, remaining[1:], global, s)
	case "search":
		return runSearch(ctx, remaining[1:], global, s)
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

type searchFlags struct {
	repo       string
	json       bool
	scope      search.Scope
	kind       string
	limit      int
	queryParts []string
}

func runSearch(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := searchFlags{
		repo:  global.repo,
		json:  global.json,
		scope: search.ScopeAll,
		limit: search.DefaultLimit,
	}
	if help, apiErr := parseSearchFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	service := core.NewService(repo)
	queryText := strings.Join(flags.queryParts, " ")
	result, err := service.Search(ctx, search.Query{
		Text: queryText, Scope: flags.scope, Kind: flags.kind, Limit: flags.limit,
	})
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write search output: %w", err))
		}
		return core.ExitOK
	}
	for _, item := range result.Results {
		title := item.Title
		if title == "" {
			title = item.ID
		}
		fmt.Fprintf(s.out, "%d. %s:%d-%d  [%s] %s  score=%d\n", item.Rank, item.Path, item.LineStart, item.LineEnd, item.Kind, title, item.Score)
		for _, line := range strings.Split(item.Snippet, "\n") {
			fmt.Fprintf(s.out, "   %s\n", line)
		}
		fmt.Fprintf(s.out, "   %s\n", item.URI)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(s.err, "warning: %s: %s\n", warning.Path, warning.Message)
	}
	return core.ExitOK
}

func parseSearchFlags(args []string, flags *searchFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, `Usage: lore [--repo PATH] search QUERY... [options]

Options:
  --scope all|pages|sources
  --kind TOKEN
  --limit N             default 10, maximum 100
  --json`)
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		case arg == "--scope" || strings.HasPrefix(arg, "--scope="):
			value, next, err := flagValue(args, index, "--scope")
			if err != nil {
				return false, err
			}
			flags.scope, index = search.Scope(value), next
		case arg == "--kind" || strings.HasPrefix(arg, "--kind="):
			value, next, err := flagValue(args, index, "--kind")
			if err != nil {
				return false, err
			}
			flags.kind, index = value, next
		case arg == "--limit" || strings.HasPrefix(arg, "--limit="):
			value, next, err := flagValue(args, index, "--limit")
			if err != nil {
				return false, err
			}
			limit, parseErr := strconv.Atoi(value)
			if parseErr != nil || limit < 1 || limit > search.MaximumLimit {
				return false, core.NewError(core.ExitUsage, "invalid_limit", fmt.Sprintf("--limit must be between 1 and %d", search.MaximumLimit))
			}
			flags.limit, index = limit, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore search: unknown flag %q", arg))
			}
			flags.queryParts = append(flags.queryParts, arg)
		}
	}
	if len(flags.queryParts) == 0 {
		return false, core.NewError(core.ExitUsage, "query_required", "lore search requires a query")
	}
	switch flags.scope {
	case search.ScopeAll, search.ScopePages, search.ScopeSources:
	default:
		return false, core.NewError(core.ExitUsage, "invalid_scope", "--scope must be all, pages, or sources")
	}
	return false, nil
}

type readFlags struct {
	repo      string
	json      bool
	reference string
	lineText  string
	lineSet   bool
}

func runRead(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := readFlags{repo: global.repo, json: global.json}
	if help, apiErr := parseReadFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	var requested *core.LineRange
	if flags.lineSet {
		value, err := core.ParseLineRange(flags.lineText)
		if err != nil {
			return emitError(s, flags.json, core.NewError(core.ExitUsage, "invalid_line_range", err.Error()))
		}
		requested = &value
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	service := core.NewService(repo)
	result, err := service.Read(ctx, flags.reference, requested)
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write read output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.err, "%s  %s  lines %d-%d  %s\n", result.Path, result.ID, result.LineStart, result.LineEnd, result.Revision)
	if _, err := io.WriteString(s.out, result.Content); err != nil {
		return emitOperationError(s, false, fmt.Errorf("write read output: %w", err))
	}
	return core.ExitOK
}

func parseReadFlags(args []string, flags *readFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, "Usage: lore [--repo PATH] read REFERENCE [--lines START:END] [--json]")
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		case arg == "--lines" || strings.HasPrefix(arg, "--lines="):
			value, next, err := flagValue(args, index, "--lines")
			if err != nil {
				return false, err
			}
			if flags.lineSet {
				return false, core.NewError(core.ExitUsage, "duplicate_flag", "--lines may be supplied only once")
			}
			flags.lineText, flags.lineSet, index = value, true, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore read: unknown flag %q", arg))
			}
			if flags.reference != "" {
				return false, core.NewError(core.ExitUsage, "unexpected_argument", "lore read accepts exactly one reference")
			}
			flags.reference = arg
		}
	}
	if flags.reference == "" {
		return false, core.NewError(core.ExitUsage, "reference_required", "lore read requires a reference")
	}
	return false, nil
}

type captureFlags struct {
	repo        string
	json        bool
	kind        string
	origin      string
	originRef   string
	sensitivity string
	tags        []string
	text        string
	textSet     bool
	file        string
	fileSet     bool
	allowEmpty  bool
	noCommit    bool
	push        *bool
}

func runCapture(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := captureFlags{
		repo:        global.repo,
		json:        global.json,
		sensitivity: "normal",
		tags:        []string{},
	}
	if apiErr := parseCaptureFlags(args, &flags, s.out); apiErr != nil {
		if apiErr.Code == "help" {
			return core.ExitOK
		}
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	body, apiErr := readCaptureBody(flags, s.in, repo.Config.Capture.MaxBytes)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	service := core.NewService(repo)
	result, err := service.Capture(ctx, core.CaptureOptions{
		Kind:        flags.kind,
		Origin:      flags.origin,
		OriginRef:   flags.originRef,
		Sensitivity: flags.sensitivity,
		Tags:        flags.tags,
		Body:        body,
		AllowEmpty:  flags.allowEmpty,
		NoCommit:    flags.noCommit,
		Push:        flags.push,
	})
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write capture output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "Captured %s\n", result.ID)
	fmt.Fprintf(s.out, "%s (%d bytes, %s)\n", result.Path, result.Bytes, result.RawSHA256)
	if result.Committed {
		fmt.Fprintf(s.out, "Committed %s\n", shortHash(result.Commit))
	} else {
		fmt.Fprintln(s.out, "Not committed")
	}
	if result.Pushed {
		fmt.Fprintln(s.out, "Pushed")
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(s.err, "warning: %s\n", warning)
	}
	return core.ExitOK
}

func parseCaptureFlags(args []string, flags *captureFlags, help io.Writer) *core.APIError {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, `Usage: lore [--repo PATH] capture --kind TOKEN --origin TOKEN [options]

Input options (mutually exclusive):
  --text STRING       capture command-line text (stdin is safer for private data)
  --file PATH         capture exact bytes from a file
  standard input      used when neither --text nor --file is supplied

Metadata and behavior:
  --origin-ref STRING
  --sensitivity normal|sensitive|local-only
  --tag STRING        repeatable
  --allow-empty
  --no-commit
  --push | --no-push
  --json`)
			return &core.APIError{ExitCode: core.ExitOK, Code: "help", Message: "", Details: map[string]any{}}
		case arg == "--json":
			flags.json = true
		case arg == "--allow-empty":
			flags.allowEmpty = true
		case arg == "--no-commit":
			flags.noCommit = true
		case arg == "--push":
			value := true
			if flags.push != nil && !*flags.push {
				return core.NewError(core.ExitUsage, "conflicting_flags", "--push and --no-push are mutually exclusive")
			}
			flags.push = &value
		case arg == "--no-push":
			value := false
			if flags.push != nil && *flags.push {
				return core.NewError(core.ExitUsage, "conflicting_flags", "--push and --no-push are mutually exclusive")
			}
			flags.push = &value
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return err
			}
			flags.repo = value
			index = next
		case arg == "--kind" || strings.HasPrefix(arg, "--kind="):
			value, next, err := flagValue(args, index, "--kind")
			if err != nil {
				return err
			}
			flags.kind = value
			index = next
		case arg == "--origin" || strings.HasPrefix(arg, "--origin="):
			value, next, err := flagValue(args, index, "--origin")
			if err != nil {
				return err
			}
			flags.origin = value
			index = next
		case arg == "--origin-ref" || strings.HasPrefix(arg, "--origin-ref="):
			value, next, err := flagValue(args, index, "--origin-ref")
			if err != nil {
				return err
			}
			flags.originRef = value
			index = next
		case arg == "--sensitivity" || strings.HasPrefix(arg, "--sensitivity="):
			value, next, err := flagValue(args, index, "--sensitivity")
			if err != nil {
				return err
			}
			flags.sensitivity = value
			index = next
		case arg == "--tag" || strings.HasPrefix(arg, "--tag="):
			value, next, err := flagValue(args, index, "--tag")
			if err != nil {
				return err
			}
			flags.tags = append(flags.tags, value)
			index = next
		case arg == "--text" || strings.HasPrefix(arg, "--text="):
			value, next, err := flagValue(args, index, "--text")
			if err != nil {
				return err
			}
			if flags.textSet {
				return core.NewError(core.ExitUsage, "duplicate_flag", "--text may be supplied only once")
			}
			flags.text, flags.textSet = value, true
			index = next
		case arg == "--file" || strings.HasPrefix(arg, "--file="):
			value, next, err := flagValue(args, index, "--file")
			if err != nil {
				return err
			}
			if flags.fileSet {
				return core.NewError(core.ExitUsage, "duplicate_flag", "--file may be supplied only once")
			}
			flags.file, flags.fileSet = value, true
			index = next
		default:
			if strings.HasPrefix(arg, "-") {
				return core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore capture: unknown flag %q", arg))
			}
			return core.NewError(core.ExitUsage, "unexpected_argument", "lore capture does not accept positional arguments")
		}
	}
	if flags.kind == "" {
		return core.NewError(core.ExitUsage, "missing_required_flag", "lore capture requires --kind")
	}
	if flags.origin == "" {
		return core.NewError(core.ExitUsage, "missing_required_flag", "lore capture requires --origin")
	}
	if flags.textSet && flags.fileSet {
		return core.NewError(core.ExitUsage, "conflicting_input", "--text and --file are mutually exclusive")
	}
	return nil
}

func flagValue(args []string, index int, name string) (string, int, *core.APIError) {
	arg := args[index]
	if strings.HasPrefix(arg, name+"=") {
		return strings.TrimPrefix(arg, name+"="), index, nil
	}
	if index+1 >= len(args) {
		return "", index, core.NewError(core.ExitUsage, "missing_flag_value", name+" requires a value")
	}
	return args[index+1], index + 1, nil
}

func readCaptureBody(flags captureFlags, stdin io.Reader, maximum int64) ([]byte, *core.APIError) {
	var reader io.Reader
	var file *os.File
	switch {
	case flags.textSet:
		reader = strings.NewReader(flags.text)
	case flags.fileSet && flags.file == "-":
		reader = stdin
	case flags.fileSet:
		opened, err := os.Open(flags.file)
		if err != nil {
			apiErr := core.NewError(core.ExitRuntime, "input_file_error", fmt.Sprintf("could not open capture input file %q", flags.file))
			apiErr.Cause = err
			return nil, apiErr
		}
		file = opened
		defer file.Close()
		reader = file
	default:
		if stdinIsTerminal(stdin) {
			return nil, core.NewError(core.ExitUsage, "capture_input_required", "capture input is required; pipe stdin or use --file/--text")
		}
		reader = stdin
	}
	body, err := captureinput.ReadBounded(reader, maximum)
	if err == nil {
		return body, nil
	}
	if errors.Is(err, captureinput.ErrTooLarge) {
		apiErr := core.NewError(core.ExitValidation, "capture_too_large", err.Error())
		apiErr.Cause = err
		return nil, apiErr
	}
	if errors.Is(err, captureinput.ErrInvalidUTF8) {
		apiErr := core.NewError(core.ExitValidation, "invalid_utf8", err.Error())
		apiErr.Cause = err
		return nil, apiErr
	}
	apiErr := core.NewError(core.ExitRuntime, "capture_input_error", "could not read capture input")
	apiErr.Cause = err
	return nil, apiErr
}

func stdinIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
		fmt.Fprintf(s.err, "lore: %s\n", apiErr.Error())
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
