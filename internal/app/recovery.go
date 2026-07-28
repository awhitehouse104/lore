package app

import (
	"context"
	"fmt"
	"io"
	"strings"

	"lore/internal/core"
	"lore/internal/output"
)

type recoverFlags struct {
	repo     string
	json     bool
	rollback bool
	finalize bool
}

func runRecover(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := recoverFlags{repo: global.repo, json: global.json || hasFlag(args, "--json")}
	if help, apiErr := parseRecoverFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	service := core.NewService(repo)
	if !flags.rollback && !flags.finalize {
		result, err := service.RecoveryStatus(ctx)
		if err != nil {
			return emitOperationError(s, flags.json, err)
		}
		if flags.json {
			if err := output.JSON(s.out, result); err != nil {
				return emitOperationError(s, false, fmt.Errorf("write recovery status output: %w", err))
			}
			return core.ExitOK
		}
		if !result.Active {
			fmt.Fprintln(s.out, "No active recovery journal.")
			return core.ExitOK
		}
		fmt.Fprintf(s.out, "Active recovery for %s\n", result.TransactionID)
		fmt.Fprintf(s.out, "Phase: %s\nBase: %s %s\n", result.Phase, result.BaseBranch, result.BaseCommit)
		if result.Commit != "" {
			fmt.Fprintf(s.out, "Commit: %s\n", result.Commit)
		}
		fmt.Fprintf(s.out, "Changed paths: %s\n", strings.Join(result.ChangedPaths, ", "))
		fmt.Fprintf(s.out, "Recommended action: %s\n", result.RecommendedAction)
		return core.ExitOK
	}
	var (
		result core.RecoveryResult
		err    error
	)
	if flags.rollback {
		result, err = service.RollbackRecovery(ctx)
	} else {
		result, err = service.FinalizeRecovery(ctx)
	}
	if err != nil {
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write recovery output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "Recovery %s completed for %s\n", result.Action, result.TransactionID)
	if result.Commit != "" {
		fmt.Fprintf(s.out, "Commit: %s\n", result.Commit)
	}
	fmt.Fprintf(s.out, "Transaction status: %s\n", result.Status)
	fmt.Fprintf(s.out, "Lint: %d error(s), %d warning(s)\n", result.Lint.Errors, result.Lint.Warnings)
	for _, warning := range result.Warnings {
		fmt.Fprintf(s.err, "warning: %s\n", warning)
	}
	return core.ExitOK
}

func parseRecoverFlags(args []string, flags *recoverFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, `Usage:
  lore [--repo PATH] recover [--json]
  lore [--repo PATH] recover --rollback [--json]
  lore [--repo PATH] recover --finalize [--json]`)
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--rollback":
			flags.rollback = true
		case arg == "--finalize":
			flags.finalize = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore recover: unknown flag %q", arg))
			}
			return false, core.NewError(core.ExitUsage, "unexpected_argument", "lore recover does not accept positional arguments")
		}
	}
	if flags.rollback && flags.finalize {
		return false, core.NewError(core.ExitUsage, "conflicting_flags", "--rollback and --finalize are mutually exclusive")
	}
	return false, nil
}
