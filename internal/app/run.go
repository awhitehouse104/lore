package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

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
		if !global.json {
			printRootUsage(stderr)
		}
		return emitError(s, global.json, core.NewError(core.ExitUsage, "command_required", "a Lore command is required"))
	}

	switch remaining[0] {
	case "init":
		return runInit(ctx, remaining[1:], global, s)
	case "capture":
		return runCapture(ctx, remaining[1:], global, s)
	case "read":
		return runRead(ctx, remaining[1:], global, s)
	case "references":
		return runReferences(ctx, remaining[1:], global, s)
	case "search":
		return runSearch(ctx, remaining[1:], global, s)
	case "recent":
		return runRecent(ctx, remaining[1:], global, s)
	case "lint":
		return runLint(ctx, remaining[1:], global, s)
	case "preview":
		return runPreview(ctx, remaining[1:], global, s)
	case "commit":
		return runCommit(ctx, remaining[1:], global, s)
	case "transaction":
		return runTransaction(ctx, remaining[1:], global, s)
	case "recover":
		return runRecover(ctx, remaining[1:], global, s)
	case "index":
		return runIndex(ctx, remaining[1:], global, s)
	case "mcp":
		return runMCP(ctx, remaining[1:], global, s)
	case "version":
		return runVersion(remaining[1:], global, s)
	case "help", "--help", "-h":
		printRootUsage(stdout)
		return core.ExitOK
	default:
		return emitError(s, global.json, core.NewError(core.ExitUsage, "unknown_command", fmt.Sprintf("unknown command %q", remaining[0])))
	}
}

type recentFlags struct {
	repo  string
	json  bool
	limit int
	all   bool
}

func runRecent(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := recentFlags{
		repo:  global.repo,
		json:  global.json || hasFlag(args, "--json"),
		limit: core.DefaultRecentLimit,
	}
	if help, apiErr := parseRecentFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	service := core.NewService(repo)
	result, err := service.Recent(ctx, core.RecentOptions{Limit: flags.limit, All: flags.all})
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write recent output: %w", err))
		}
		return core.ExitOK
	}
	for _, commit := range result.Commits {
		fmt.Fprintf(s.out, "%s  %s  %s  %s <%s>\n",
			shortHash(commit.Hash), commit.CommittedAt.Format(time.RFC3339), commit.Subject, commit.AuthorName, commit.AuthorEmail)
	}
	return core.ExitOK
}

func parseRecentFlags(args []string, flags *recentFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, "Usage: lore [--repo PATH] recent [--limit N] [--all] [--json]")
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--all":
			flags.all = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		case arg == "--limit" || strings.HasPrefix(arg, "--limit="):
			value, next, err := flagValue(args, index, "--limit")
			if err != nil {
				return false, err
			}
			limit, parseErr := strconv.Atoi(value)
			if parseErr != nil || limit < 1 || limit > core.MaximumRecentLimit {
				return false, core.NewError(core.ExitUsage, "invalid_limit", fmt.Sprintf("--limit must be between 1 and %d", core.MaximumRecentLimit))
			}
			flags.limit, index = limit, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore recent: unknown flag %q", arg))
			}
			return false, core.NewError(core.ExitUsage, "unexpected_argument", "lore recent does not accept positional arguments")
		}
	}
	return false, nil
}

type searchFlags struct {
	repo                 string
	json                 bool
	scope                search.Scope
	kind                 string
	limit                int
	backend              search.Backend
	matching             search.MatchingMode
	includeSensitivities []string
	queryParts           []string
}

func runSearch(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := searchFlags{
		repo:  global.repo,
		json:  global.json || hasFlag(args, "--json"),
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
	backend := flags.backend
	if backend == "" {
		backend = search.Backend(repo.Config.Index.Backend)
	}
	access := search.AllAccessPolicy()
	if len(flags.includeSensitivities) > 0 {
		var err error
		access, err = search.NewAccessPolicy(flags.includeSensitivities)
		if err != nil {
			return emitError(s, flags.json, core.NewError(core.ExitUsage, "invalid_sensitivity", err.Error()))
		}
	}
	queryText := strings.Join(flags.queryParts, " ")
	result, err := service.Search(ctx, search.Query{
		Text:     queryText,
		Scope:    flags.scope,
		Kind:     flags.kind,
		Limit:    flags.limit,
		Backend:  backend,
		Matching: flags.matching,
		Access:   access,
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
		for _, match := range item.FuzzyMatches {
			fmt.Fprintf(s.out, "   fuzzy: %s -> %s (distance %d)\n", match.QueryTerm, match.DocumentTerm, match.Distance)
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
  --backend auto|index|filesystem
  --matching auto|lexical|fuzzy
  --include-sensitivity normal|sensitive|local-only
                        repeatable; defaults to all local sensitivities
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
		case arg == "--backend" || strings.HasPrefix(arg, "--backend="):
			value, next, err := flagValue(args, index, "--backend")
			if err != nil {
				return false, err
			}
			flags.backend, index = search.Backend(value), next
		case arg == "--matching" || strings.HasPrefix(arg, "--matching="):
			value, next, err := flagValue(args, index, "--matching")
			if err != nil {
				return false, err
			}
			flags.matching, index = search.MatchingMode(value), next
		case arg == "--include-sensitivity" || strings.HasPrefix(arg, "--include-sensitivity="):
			value, next, err := flagValue(args, index, "--include-sensitivity")
			if err != nil {
				return false, err
			}
			flags.includeSensitivities = append(flags.includeSensitivities, value)
			index = next
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
	switch flags.backend {
	case "", search.BackendAuto, search.BackendIndex, search.BackendFilesystem:
	default:
		return false, core.NewError(core.ExitUsage, "invalid_backend", "--backend must be auto, index, or filesystem")
	}
	switch flags.matching {
	case "", search.MatchingAuto, search.MatchingLexical, search.MatchingFuzzy:
	default:
		return false, core.NewError(core.ExitUsage, "invalid_matching", "--matching must be auto, lexical, or fuzzy")
	}
	for _, sensitivity := range flags.includeSensitivities {
		if sensitivity != "normal" && sensitivity != "sensitive" && sensitivity != "local-only" {
			return false, core.NewError(core.ExitUsage, "invalid_sensitivity", "--include-sensitivity must be normal, sensitive, or local-only")
		}
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
	flags.json = flags.json || hasFlag(args, "--json")
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

type referencesFlags struct {
	repo      string
	json      bool
	reference string
}

func runReferences(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := referencesFlags{repo: global.repo, json: global.json || hasFlag(args, "--json")}
	if help, apiErr := parseReferencesFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	result, err := core.NewService(repo).PageReferences(ctx, flags.reference)
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write references output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "%s  %s  %s\n", result.Target.Path, result.Target.ID, result.Target.Revision)
	fmt.Fprintln(s.out, "\nLive backlinks:")
	if len(result.LiveBacklinks) == 0 {
		fmt.Fprintln(s.out, "  none")
	}
	for _, reference := range result.LiveBacklinks {
		fmt.Fprintf(s.out, "  %s:%d  %s  %s\n", reference.Path, reference.Line, reference.ID, reference.Destination)
	}
	fmt.Fprintln(s.out, "\nHistorical source mentions:")
	if len(result.HistoricalSourceMentions) == 0 {
		fmt.Fprintln(s.out, "  none")
	}
	for _, reference := range result.HistoricalSourceMentions {
		fmt.Fprintf(s.out, "  %s:%d  %s  %s\n", reference.Path, reference.Line, reference.ID, reference.Destination)
	}
	fmt.Fprintln(s.out, "\nSource integrations:")
	if len(result.SourceIntegrations) == 0 {
		fmt.Fprintln(s.out, "  none")
	}
	for _, reference := range result.SourceIntegrations {
		fmt.Fprintf(s.out, "  %s:%d  %s\n", reference.Path, reference.Line, reference.ID)
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(s.err, "warning: %s: %s\n", warning.Path, warning.Message)
	}
	return core.ExitOK
}

func parseReferencesFlags(args []string, flags *referencesFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, "Usage: lore [--repo PATH] references PAGE_REFERENCE [--json]")
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore references: unknown flag %q", arg))
			}
			if flags.reference != "" {
				return false, core.NewError(core.ExitUsage, "unexpected_argument", "lore references accepts exactly one page reference")
			}
			flags.reference = arg
		}
	}
	if flags.reference == "" {
		return false, core.NewError(core.ExitUsage, "reference_required", "lore references requires a page reference")
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
		repo: global.repo,
		json: global.json || hasFlag(args, "--json"),
		tags: []string{},
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

Required metadata:
  --sensitivity normal|sensitive|local-only

Optional metadata and behavior:
  --origin-ref STRING
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
	if flags.sensitivity == "" {
		return core.NewError(core.ExitUsage, "missing_required_flag", "lore capture requires --sensitivity")
	}
	if flags.textSet && flags.fileSet {
		return core.NewError(core.ExitUsage, "conflicting_input", "--text and --file are mutually exclusive")
	}
	return nil
}

func flagValue(args []string, index int, name string) (string, int, *core.APIError) {
	arg := args[index]
	if strings.HasPrefix(arg, name+"=") {
		value := strings.TrimPrefix(arg, name+"=")
		if value == "" && name != "--text" && name != "--origin-ref" && name != "--tag" {
			return "", index, core.NewError(core.ExitUsage, "missing_flag_value", name+" requires a value")
		}
		return value, index, nil
	}
	if index+1 >= len(args) {
		return "", index, core.NewError(core.ExitUsage, "missing_flag_value", name+" requires a value")
	}
	value := args[index+1]
	if value == "" && name != "--text" && name != "--origin-ref" && name != "--tag" {
		return "", index, core.NewError(core.ExitUsage, "missing_flag_value", name+" requires a value")
	}
	return value, index + 1, nil
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
	jsonOutput := global.json || hasFlag(args, "--json")
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
	repoPath := global.repo
	jsonOutput := global.json || hasFlag(args, "--json")
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore [--repo PATH] lint [--json]")
			return core.ExitOK
		case arg == "--json":
			jsonOutput = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, apiErr := flagValue(args, index, "--repo")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			repoPath, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore lint: unknown flag %q", arg)))
			}
			return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore lint does not accept positional arguments"))
		}
	}
	root, apiErr := resolveRepositoryRoot(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	repo, err := repository.Open(root)
	if err != nil {
		result, lintErr := lint.RunRoot(ctx, root, gitx.New())
		if lintErr != nil {
			return emitOperationError(s, jsonOutput, lintErr)
		}
		return emitLintResult(s, jsonOutput, result)
	}
	result, err := core.NewService(repo).Lint(ctx)
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	return emitLintResult(s, jsonOutput, result)
}

func emitLintResult(s streams, jsonOutput bool, result lint.Result) int {
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
	jsonOutput := global.json || hasFlag(args, "--json")
	for _, arg := range args {
		switch arg {
		case "--help", "-h":
			fmt.Fprintln(s.out, "Usage: lore version [--json]")
			return core.ExitOK
		case "--json":
			jsonOutput = true
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore version: unknown flag %q", arg)))
			}
			return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore version does not accept positional arguments"))
		}
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
	root, apiErr := resolveRepositoryRoot(explicit)
	if apiErr != nil {
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

func resolveRepositoryRoot(explicit string) (string, *core.APIError) {
	cwd, err := os.Getwd()
	if err != nil {
		apiErr := core.NewError(core.ExitRuntime, "filesystem_error", "could not determine the current directory")
		apiErr.Cause = err
		return "", apiErr
	}
	root, err := repository.Resolve(explicit, cwd, os.Getenv)
	if err != nil {
		apiErr := core.NewError(core.ExitUsage, "repository_not_found", err.Error())
		apiErr.Cause = err
		return "", apiErr
	}
	return root, nil
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

func hasFlag(args []string, target string) bool {
	for _, arg := range args {
		if arg == target {
			return true
		}
	}
	return false
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
  references
            inspect live backlinks and historical source references to a page
  lint      validate repository integrity
  preview   validate and persist an exact transaction proposal
  commit    apply and Git-commit a previewed transaction
  transaction
            inspect, discard, or prune transaction artifacts
  recover   inspect, roll back, or finalize interrupted transactions
  index     build, update, inspect, or clear the derived search index
  mcp       serve Lore through the Model Context Protocol
  recent    inspect recent knowledge commits
  version   print build version

Run "lore <command> --help" for command-specific help.`)
}
