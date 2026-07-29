package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"lore/internal/core"
	"lore/internal/mcpserver"
)

type mcpStdioFlags struct {
	repo      string
	profile   string
	logFormat string
}

func runMCP(ctx context.Context, args []string, global globalOptions, s streams) int {
	if len(args) == 0 {
		return emitError(s, false, core.NewError(core.ExitUsage, "mcp_command_required", "lore mcp requires stdio, serve, or check-config"))
	}
	switch args[0] {
	case "stdio":
		return runMCPStdio(ctx, args[1:], global, s)
	case "help", "--help", "-h":
		printMCPUsage(s.out)
		return core.ExitOK
	case "serve", "check-config":
		return emitError(s, false, core.NewError(core.ExitUsage, "mcp_command_unavailable", fmt.Sprintf("lore mcp %s is planned for v0.4 Milestone 3", args[0])))
	default:
		return emitError(s, false, core.NewError(core.ExitUsage, "unknown_mcp_command", fmt.Sprintf("unknown MCP command %q", args[0])))
	}
}

func runMCPStdio(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := mcpStdioFlags{
		repo:      global.repo,
		profile:   "local-full",
		logFormat: "text",
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore mcp stdio --repo PATH [--profile local-full] [--log-format json|text]")
			return core.ExitOK
		case arg == "--repo" || strings.HasPrefix(arg, "--repo="):
			value, next, apiErr := flagValue(args, index, "--repo")
			if apiErr != nil {
				return emitError(s, false, apiErr)
			}
			flags.repo, index = value, next
		case arg == "--profile" || strings.HasPrefix(arg, "--profile="):
			value, next, apiErr := flagValue(args, index, "--profile")
			if apiErr != nil {
				return emitError(s, false, apiErr)
			}
			flags.profile, index = value, next
		case arg == "--log-format" || strings.HasPrefix(arg, "--log-format="):
			value, next, apiErr := flagValue(args, index, "--log-format")
			if apiErr != nil {
				return emitError(s, false, apiErr)
			}
			flags.logFormat, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return emitError(s, false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore mcp stdio: unknown flag %q", arg)))
			}
			return emitError(s, false, core.NewError(core.ExitUsage, "unexpected_argument", "lore mcp stdio does not accept positional arguments"))
		}
	}
	if flags.profile != "local-full" {
		return emitError(s, false, core.NewError(core.ExitUsage, "unknown_local_profile", "v0.4 Milestone 1 supports only the local-full profile"))
	}
	if flags.logFormat != "text" && flags.logFormat != "json" {
		return emitError(s, false, core.NewError(core.ExitUsage, "invalid_log_format", "--log-format must be json or text"))
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, false, apiErr)
	}
	logger := stdioLogger(s.err, flags.logFormat)
	err := mcpserver.RunStdio(ctx, core.NewService(repo), s.in, s.out, logger)
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
		return core.ExitOK
	}
	return emitOperationError(s, false, fmt.Errorf("MCP stdio server failed: %w", err))
}

func stdioLogger(output io.Writer, format string) *slog.Logger {
	options := &slog.HandlerOptions{Level: slog.LevelWarn}
	if format == "json" {
		return slog.New(slog.NewJSONHandler(output, options))
	}
	return slog.New(slog.NewTextHandler(output, options))
}

func printMCPUsage(w io.Writer) {
	fmt.Fprintln(w, `Usage: lore mcp <command> [options]

Commands:
  stdio         serve one Lore repository over stdin/stdout
  serve         serve stateless Streamable HTTP (Milestone 3)
  check-config  validate external server configuration (Milestone 3)`)
}
