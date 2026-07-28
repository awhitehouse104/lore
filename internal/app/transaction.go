package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"lore/internal/core"
	"lore/internal/output"
	"lore/internal/transaction"
)

type previewFlags struct {
	repo     string
	json     bool
	input    string
	inputSet bool
}

func runPreview(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := previewFlags{repo: global.repo, json: global.json || hasFlag(args, "--json")}
	if help, apiErr := parsePreviewFlags(args, &flags, s.out); help {
		return core.ExitOK
	} else if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	request, apiErr := readPreviewRequest(flags, s.in)
	if apiErr != nil {
		return emitError(s, flags.json, apiErr)
	}
	result, err := core.NewService(repo).Preview(ctx, request)
	if err != nil {
		var operationErr *core.APIError
		if errors.As(err, &operationErr) && operationErr.Code == "prospective_lint_invalid" {
			if flags.json {
				if writeErr := output.JSON(s.out, result); writeErr != nil {
					return emitOperationError(s, false, fmt.Errorf("write preview output: %w", writeErr))
				}
			} else {
				printPreview(s, result)
			}
			return core.ExitValidation
		}
		return emitOperationError(s, flags.json, err)
	}
	if flags.json {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write preview output: %w", err))
		}
		return core.ExitOK
	}
	printPreview(s, result)
	return core.ExitOK
}

func parsePreviewFlags(args []string, flags *previewFlags, help io.Writer) (bool, *core.APIError) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(help, "Usage: lore [--repo PATH] preview [--input PATH|-] [--json]")
			return true, nil
		case arg == "--json":
			flags.json = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, err := flagValue(args, index, "--repo")
			if err != nil {
				return false, err
			}
			flags.repo, index = value, next
		case arg == "--input" || strings.HasPrefix(arg, "--input="):
			value, next, err := flagValue(args, index, "--input")
			if err != nil {
				return false, err
			}
			if flags.inputSet {
				return false, core.NewError(core.ExitUsage, "duplicate_flag", "--input may be supplied only once")
			}
			flags.input, flags.inputSet, index = value, true, next
		default:
			if strings.HasPrefix(arg, "-") {
				return false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore preview: unknown flag %q", arg))
			}
			return false, core.NewError(core.ExitUsage, "unexpected_argument", "lore preview does not accept positional arguments")
		}
	}
	return false, nil
}

func readPreviewRequest(flags previewFlags, stdin io.Reader) ([]byte, *core.APIError) {
	var reader io.Reader
	var file *os.File
	switch {
	case flags.inputSet && flags.input != "-":
		opened, err := os.Open(flags.input)
		if err != nil {
			apiErr := core.NewError(core.ExitRuntime, "input_file_error", fmt.Sprintf("could not open transaction input file %q", flags.input))
			apiErr.Cause = err
			return nil, apiErr
		}
		file = opened
		defer file.Close()
		reader = file
	case flags.inputSet:
		reader = stdin
	default:
		if stdinIsTerminal(stdin) {
			return nil, core.NewError(core.ExitUsage, "preview_input_required", "transaction input is required; pipe stdin or use --input")
		}
		reader = stdin
	}
	data, err := io.ReadAll(io.LimitReader(reader, transaction.MaxRequestBytes+1))
	if err != nil {
		apiErr := core.NewError(core.ExitRuntime, "preview_input_error", "could not read transaction input")
		apiErr.Cause = err
		return nil, apiErr
	}
	if len(data) > transaction.MaxRequestBytes {
		return nil, core.NewError(core.ExitValidation, "request_too_large", fmt.Sprintf("transaction request exceeds %d bytes", transaction.MaxRequestBytes))
	}
	return data, nil
}

func printPreview(s streams, result core.PreviewResult) {
	if result.TransactionID != "" {
		fmt.Fprintf(s.out, "Transaction %s (%s)\n", result.TransactionID, result.Status)
		fmt.Fprintf(s.out, "Preview digest: %s\n", result.PreviewDigest)
	} else {
		fmt.Fprintf(s.out, "Transaction preview: %s (not persisted)\n", result.Status)
	}
	fmt.Fprintf(s.out, "Base: %s %s\n", result.BaseBranch, result.BaseCommit)
	fmt.Fprintf(s.out, "Changed paths: %s\n", strings.Join(result.ChangedPaths, ", "))
	fmt.Fprintf(s.out, "Lint: %d error(s), %d warning(s)\n", result.Lint.Errors, result.Lint.Warnings)
	for _, finding := range result.Lint.Findings {
		location := finding.Path
		if finding.Line > 0 {
			location = fmt.Sprintf("%s:%d", location, finding.Line)
		}
		fmt.Fprintf(s.out, "%s: %s: %s [%s]\n", finding.Severity, location, finding.Message, finding.Code)
	}
	if result.Diff != "" {
		fmt.Fprintln(s.out)
		_, _ = io.WriteString(s.out, result.Diff)
		if !strings.HasSuffix(result.Diff, "\n") {
			fmt.Fprintln(s.out)
		}
	}
}

func runTransaction(ctx context.Context, args []string, global globalOptions, s streams) int {
	jsonOutput := global.json || hasFlag(args, "--json")
	if len(args) == 0 {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "subcommand_required", "lore transaction requires list, show, or discard"))
	}
	switch args[0] {
	case "list":
		return runTransactionList(args[1:], global, s)
	case "show":
		return runTransactionShow(args[1:], global, s)
	case "discard":
		return runTransactionDiscard(args[1:], global, s)
	case "--help", "-h", "help":
		printTransactionUsage(s.out)
		return core.ExitOK
	default:
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_subcommand", fmt.Sprintf("unknown transaction subcommand %q", args[0])))
	}
}

func runTransactionList(args []string, global globalOptions, s streams) int {
	repoPath := global.repo
	jsonOutput := global.json || hasFlag(args, "--json")
	limit := core.DefaultTransactionLimit
	var status transaction.Status
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore [--repo PATH] transaction list [--status STATUS] [--limit N] [--json]")
			return core.ExitOK
		case arg == "--json":
			jsonOutput = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, apiErr := flagValue(args, index, "--repo")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			repoPath, index = value, next
		case arg == "--status" || strings.HasPrefix(arg, "--status="):
			value, next, apiErr := flagValue(args, index, "--status")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			status, index = transaction.Status(value), next
		case arg == "--limit" || strings.HasPrefix(arg, "--limit="):
			value, next, apiErr := flagValue(args, index, "--limit")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || parsed < 1 || parsed > core.MaximumTransactionLimit {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "invalid_limit", fmt.Sprintf("--limit must be between 1 and %d", core.MaximumTransactionLimit)))
			}
			limit, index = parsed, next
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore transaction list: unknown flag %q", arg)))
			}
			return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore transaction list does not accept positional arguments"))
		}
	}
	if status != "" && !listableStatus(status) {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "invalid_status", "--status must be previewed, committed, discarded, failed, or recovery_required"))
	}
	repo, apiErr := openRepository(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	result, err := core.NewService(repo).TransactionList(status, limit)
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write transaction list output: %w", err))
		}
		return core.ExitOK
	}
	for _, item := range result.Transactions {
		fmt.Fprintf(s.out, "%s  %-17s  %s  %s\n", item.TransactionID, item.Status, item.CreatedAt, item.Message)
	}
	return core.ExitOK
}

func runTransactionShow(args []string, global globalOptions, s streams) int {
	repoPath := global.repo
	jsonOutput := global.json || hasFlag(args, "--json")
	includeDiff := false
	var transactionID string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore [--repo PATH] transaction show TRANSACTION_ID [--diff] [--json]")
			return core.ExitOK
		case arg == "--json":
			jsonOutput = true
		case arg == "--diff":
			includeDiff = true
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, apiErr := flagValue(args, index, "--repo")
			if apiErr != nil {
				return emitError(s, jsonOutput, apiErr)
			}
			repoPath, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore transaction show: unknown flag %q", arg)))
			}
			if transactionID != "" {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore transaction show accepts exactly one transaction ID"))
			}
			transactionID = arg
		}
	}
	if transactionID == "" {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "transaction_id_required", "lore transaction show requires a transaction ID"))
	}
	repo, apiErr := openRepository(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	result, err := core.NewService(repo).TransactionShow(transactionID, includeDiff)
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write transaction show output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "Transaction %s (%s)\n", result.Proposal.TransactionID, result.State.Status)
	fmt.Fprintf(s.out, "Created: %s\nUpdated: %s\n", result.Proposal.CreatedAt, result.State.UpdatedAt)
	fmt.Fprintf(s.out, "Actor: %s\nMessage: %s\n", result.Proposal.Actor, result.Proposal.Message)
	fmt.Fprintf(s.out, "Base: %s %s\n", result.Proposal.BaseBranch, result.Proposal.BaseCommit)
	fmt.Fprintf(s.out, "Preview digest: %s\n", result.PreviewDigest)
	fmt.Fprintf(s.out, "Diff SHA-256: %s\nLint SHA-256: %s\n", result.Proposal.DiffSHA256, result.Proposal.LintSHA256)
	fmt.Fprintf(s.out, "Lint: %d error(s), %d warning(s)\n", result.Lint.Errors, result.Lint.Warnings)
	fmt.Fprintf(s.out, "Changed paths: %s\n", strings.Join(result.Proposal.ChangedPaths, ", "))
	if includeDiff && result.Diff != "" {
		fmt.Fprintln(s.out)
		_, _ = io.WriteString(s.out, result.Diff)
	}
	return core.ExitOK
}

func runTransactionDiscard(args []string, global globalOptions, s streams) int {
	repoPath := global.repo
	jsonOutput := global.json || hasFlag(args, "--json")
	var transactionID string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore [--repo PATH] transaction discard TRANSACTION_ID [--json]")
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
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore transaction discard: unknown flag %q", arg)))
			}
			if transactionID != "" {
				return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "unexpected_argument", "lore transaction discard accepts exactly one transaction ID"))
			}
			transactionID = arg
		}
	}
	if transactionID == "" {
		return emitError(s, jsonOutput, core.NewError(core.ExitUsage, "transaction_id_required", "lore transaction discard requires a transaction ID"))
	}
	repo, apiErr := openRepository(repoPath)
	if apiErr != nil {
		return emitError(s, jsonOutput, apiErr)
	}
	result, err := core.NewService(repo).TransactionDiscard(transactionID)
	if err != nil {
		return emitOperationError(s, jsonOutput, err)
	}
	if jsonOutput {
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, fmt.Errorf("write transaction discard output: %w", err))
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "Discarded transaction %s\n", result.TransactionID)
	return core.ExitOK
}

func listableStatus(status transaction.Status) bool {
	switch status {
	case transaction.StatusPreviewed, transaction.StatusCommitted, transaction.StatusDiscarded,
		transaction.StatusFailed, transaction.StatusRecoveryRequired:
		return true
	default:
		return false
	}
}

func printTransactionUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: lore [--repo PATH] transaction <subcommand> [options]

Subcommands:
  list       list transaction metadata
  show       verify and inspect a transaction
  discard    discard a previewed or failed transaction`)
}
