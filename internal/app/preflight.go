package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"lore/internal/core"
	"lore/internal/output"
)

type preflightFlags struct {
	repo   string
	json   bool
	sync   bool
	deep   bool
	branch string
}

func runPreflight(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := preflightFlags{
		repo:   global.repo,
		json:   global.json || hasFlag(args, "--json"),
		branch: core.DefaultPreflightBranch,
	}
	if help, apiErr := parsePreflightFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	result, err := core.NewService(repo).Preflight(ctx, core.PreflightOptions{
		Sync: flags.sync, Deep: flags.deep, Branch: flags.branch,
	})
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write preflight output: %w", err))
		}
	} else {
		printPreflightResult(s, result)
	}
	if !result.Ready {
		return core.ExitConflict
	}
	return core.ExitOK
}

func parsePreflightFlags(args []string, flags *preflightFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, `Usage: lore [--repo PATH] preflight [--sync] [--deep] [--branch NAME] [--json]

Checks the current branch, complete worktree, recovery journal, pending previews,
and derived index in one operation. With --sync, fetches the configured remote
once and fast-forwards from that fetched ref only when local history is behind.
Use --deep for a full lint and index verification even when HEAD is unchanged.`)
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--sync":
			flags.sync = true
		case arg == "--deep":
			flags.deep = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		case arg == "--branch" || strings.HasPrefix(arg, "--branch="):
			value, next, err := flagValue(args, index, "--branch")
			if err != nil {
				return false, err
			}
			flags.branch, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore preflight: unknown flag %q", arg))
			}
			return false, core.NewError(core.ExitUsage, "unexpected_argument", "lore preflight does not accept positional arguments")
		}
	}
	return false, nil
}

func printPreflightResult(s streams, result core.PreflightResult) {
	if result.Ready {
		fmt.Fprintf(s.out, "Lore preflight: ready (%s)\n", result.Scope)
	} else {
		fmt.Fprintf(s.out, "Lore preflight: blocked (%s)\n", result.Scope)
	}
	if result.RepositoryRoot != "" {
		fmt.Fprintf(s.out, "Repository: %s\n", result.RepositoryRoot)
	}
	if result.Local.Branch != "" {
		fmt.Fprintf(s.out, "Branch: %s\n", result.Local.Branch)
	}
	if result.Remote.Checked {
		fmt.Fprintf(s.out, "Remote: %s/%s (ahead %d, behind %d", result.Remote.Remote, result.Remote.Branch, result.Remote.Ahead, result.Remote.Behind)
		if result.Remote.FastForwarded {
			fmt.Fprint(s.out, ", fast-forwarded")
		}
		fmt.Fprintln(s.out, ")")
	}
	if result.Index != nil {
		fmt.Fprintf(s.out, "Index: %s (%s)\n", result.Index.IndexState, result.IndexAction)
	}
	if result.Lint != nil {
		fmt.Fprintf(s.out, "Lint: %d error(s), %d warning(s)\n", result.Lint.Errors, result.Lint.Warnings)
	}
	for _, blocker := range result.Blockers {
		fmt.Fprintf(s.err, "blocked: %s: %s\n", blocker.Code, blocker.Message)
		if blocker.Action != "" {
			fmt.Fprintf(s.err, "  action: %s\n", blocker.Action)
		}
		for _, change := range blocker.Changes {
			fmt.Fprintf(s.err, "  %s %s\n", change.Status, change.Path)
		}
	}
}
