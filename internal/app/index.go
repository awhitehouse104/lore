package app

import (
	"context"
	"fmt"
	"strings"

	"lore/internal/core"
	loreindex "lore/internal/index"
	"lore/internal/output"
)

func runIndex(ctx context.Context, args []string, global globalOptions, s streams) int {
	jsonOutput := global.json || hasFlag(args, "--json")
	if len(args) == 0 {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "index_command_required", "lore index requires build, update, status, or clear"))
	}
	switch args[0] {
	case "build":
		return runIndexBuild(ctx, args[1:], global, s)
	case "status":
		return runIndexStatus(ctx, args[1:], global, s)
	case "help", "--help", "-h":
		printIndexUsage(s.out)
		return core.ExitOK
	case "update", "clear":
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "index_command_unavailable", fmt.Sprintf("lore index %s is not available in this milestone", args[0])))
	default:
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_index_command", fmt.Sprintf("unknown index command %q", args[0])))
	}
}

func runIndexBuild(ctx context.Context, args []string, global globalOptions, s streams) int {
	repoPath := global.repo
	jsonOutput := global.json || hasFlag(args, "--json")
	force := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore [--repo PATH] index build [--force] [--json]")
			return core.ExitOK
		case arg == "--json":
			jsonOutput = true
		case arg == "--force":
			force = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, apiErr := flagValue(args, index, "--repo")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			repoPath, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore index build: unknown flag %q", arg)))
			}
			return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore index build does not accept positional arguments"))
		}
	}
	repo, apiErr := openRepository(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	result, err := core.NewService(repo).IndexBuild(ctx, core.IndexBuildOptions{Force: force})
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write index build output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "Built %s with %d document(s): %d page(s), %d source(s)\n", result.Path, result.DocumentCount, result.PageCount, result.SourceCount)
	fmt.Fprintf(s.out, "State: %s; snapshot: %s\n", result.IndexState, displaySnapshot(result.IndexedBranch, result.IndexedHead))
	for _, warning := range result.Warnings {
		fmt.Fprintf(s.err, "warning: %s: %s\n", warning.Code, warning.Message)
	}
	return core.ExitOK
}

func runIndexStatus(ctx context.Context, args []string, global globalOptions, s streams) int {
	repoPath := global.repo
	jsonOutput := global.json || hasFlag(args, "--json")
	verify := false
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore [--repo PATH] index status [--verify] [--json]")
			return core.ExitOK
		case arg == "--json":
			jsonOutput = true
		case arg == "--verify":
			verify = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, apiErr := flagValue(args, index, "--repo")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			repoPath, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore index status: unknown flag %q", arg)))
			}
			return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore index status does not accept positional arguments"))
		}
	}
	repo, apiErr := openRepository(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	result, err := core.NewService(repo).IndexStatus(ctx, verify)
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write index status output: %w", err))
		}
	} else {
		fmt.Fprintf(s.out, "Index: %s\n", result.IndexState)
		fmt.Fprintf(s.out, "Path: %s\n", result.Path)
		if result.IndexState != loreindex.StateMissing {
			fmt.Fprintf(s.out, "Documents: %d (%d pages, %d sources)\n", result.DocumentCount, result.PageCount, result.SourceCount)
			fmt.Fprintf(s.out, "Snapshot: %s\n", displaySnapshot(result.IndexedBranch, result.IndexedHead))
			fmt.Fprintf(s.out, "Verification: %s\n", result.Verification)
		}
		for _, warning := range result.Warnings {
			fmt.Fprintf(s.err, "warning: %s: %s\n", warning.Code, warning.Message)
		}
	}
	if result.IndexState == loreindex.StateCorrupt || result.IndexState == loreindex.StateIncompatible {
		return core.ExitRuntime
	}
	return core.ExitOK
}

func displaySnapshot(branch, head string) string {
	if branch == "" && head == "" {
		return "non-Git"
	}
	if head == "" {
		return branch + " (no commits)"
	}
	return branch + "@" + shortHash(head)
}

func printIndexUsage(w interface{ Write([]byte) (int, error) }) {
	fmt.Fprint(w, `Usage: lore [--repo PATH] index <command> [options]

Commands:
  build    build and verify a complete replacement index
  update   reconcile an existing compatible index
  status   inspect index freshness and integrity
  clear    remove derived index files
`)
}
