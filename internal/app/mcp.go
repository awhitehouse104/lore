package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"lore/internal/auth"
	"lore/internal/core"
	"lore/internal/mcpserver"
	"lore/internal/output"
	"lore/internal/serverconfig"
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
	case "serve":
		return runMCPServe(ctx, args[1:], global, s)
	case "check-config":
		return runMCPCheckConfig(args[1:], global, s)
	case "help", "--help", "-h":
		printMCPUsage(s.out)
		return core.ExitOK
	default:
		return emitError(s, false, core.NewError(core.ExitUsage, "unknown_mcp_command", fmt.Sprintf("unknown MCP command %q", args[0])))
	}
}

type mcpConfigFlags struct {
	config string
}

func runMCPCheckConfig(args []string, global globalOptions, s streams) int {
	flags, help, apiErr := parseMCPConfigFlags(args, "check-config", global, s.out)
	if help {
		return core.ExitOK
	}
	if apiErr != nil {
		return emitError(s, global.json, apiErr)
	}
	config, err := serverconfig.Load(flags.config)
	if err != nil {
		return emitError(s, global.json, core.NewError(core.ExitUsage, "invalid_server_config", err.Error()))
	}
	if global.json {
		result := struct {
			SchemaVersion int    `json:"schema_version"`
			Status        string `json:"status"`
			Listen        string `json:"listen"`
			Endpoint      string `json:"endpoint"`
			Principals    int    `json:"principals"`
		}{
			SchemaVersion: core.SchemaVersion,
			Status:        "ok",
			Listen:        config.Listen,
			Endpoint:      config.Endpoint,
			Principals:    len(config.BearerPrincipals),
		}
		if err := output.JSON(s.out, result); err != nil {
			return emitOperationError(s, false, err)
		}
		return core.ExitOK
	}
	fmt.Fprintln(s.out, "MCP server configuration is valid.")
	return core.ExitOK
}

func runMCPServe(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags, help, apiErr := parseMCPConfigFlags(args, "serve", global, s.out)
	if help {
		return core.ExitOK
	}
	if apiErr != nil {
		return emitError(s, false, apiErr)
	}
	config, err := serverconfig.Load(flags.config)
	if err != nil {
		return emitError(s, false, core.NewError(core.ExitUsage, "invalid_server_config", err.Error()))
	}
	repo, apiErr := openRepository(config.Repo)
	if apiErr != nil {
		return emitError(s, false, apiErr)
	}
	logger := serverLogger(s.err, config)
	logger.Info("MCP HTTP server starting",
		"listen", config.Listen,
		"endpoint", config.Endpoint,
		"stateless", true,
	)
	server := mcpserver.NewHTTPService(core.NewService(repo), config, logger)
	if err := server.ListenAndServe(ctx); err != nil {
		return emitOperationError(s, false, fmt.Errorf("MCP HTTP server failed: %w", err))
	}
	return core.ExitOK
}

func parseMCPConfigFlags(args []string, command string, global globalOptions, helpOutput io.Writer) (mcpConfigFlags, bool, *core.APIError) {
	flags := mcpConfigFlags{config: serverconfig.DefaultPath}
	if global.repo != "" {
		return flags, false, core.NewError(core.ExitUsage, "repo_flag_not_supported", "MCP HTTP commands take the repository only from the external server configuration")
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintf(helpOutput, "Usage: lore mcp %s [--config PATH]\n", command)
			return flags, true, nil
		case arg == "--config" || strings.HasPrefix(arg, "--config="):
			value, next, apiErr := flagValue(args, index, "--config")
			if apiErr != nil {
				return flags, false, apiErr
			}
			if value == "" {
				return flags, false, core.NewError(core.ExitUsage, "invalid_config_path", "--config must not be empty")
			}
			flags.config, index = value, next
		default:
			if strings.HasPrefix(arg, "-") {
				return flags, false, core.NewError(core.ExitUsage, "unknown_flag", fmt.Sprintf("lore mcp %s: unknown flag %q", command, arg))
			}
			return flags, false, core.NewError(core.ExitUsage, "unexpected_argument", fmt.Sprintf("lore mcp %s does not accept positional arguments", command))
		}
	}
	return flags, false, nil
}

func serverLogger(output io.Writer, config serverconfig.Config) *slog.Logger {
	var level slog.Level
	switch config.Logging.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	options := &slog.HandlerOptions{Level: level}
	if config.Logging.Format == "text" {
		return slog.New(slog.NewTextHandler(output, options))
	}
	return slog.New(slog.NewJSONHandler(output, options))
}

func runMCPStdio(ctx context.Context, args []string, global globalOptions, s streams) int {
	flags := mcpStdioFlags{
		repo:      global.repo,
		profile:   auth.DefaultLocalProfile,
		logFormat: "text",
	}
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--help" || arg == "-h":
			fmt.Fprintln(s.out, "Usage: lore mcp stdio --repo PATH [--profile local-full|local-query] [--log-format json|text]")
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
	principal, err := auth.LocalProfile(flags.profile)
	if err != nil {
		return emitError(s, false, core.NewError(core.ExitUsage, "unknown_local_profile", err.Error()))
	}
	if flags.logFormat != "text" && flags.logFormat != "json" {
		return emitError(s, false, core.NewError(core.ExitUsage, "invalid_log_format", "--log-format must be json or text"))
	}
	repo, apiErr := openRepository(flags.repo)
	if apiErr != nil {
		return emitError(s, false, apiErr)
	}
	logger := stdioLogger(s.err, flags.logFormat)
	err = mcpserver.RunStdio(ctx, core.NewService(repo), principal, s.in, s.out, logger)
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
  serve         serve stateless Streamable HTTP
  check-config  validate external server configuration and token files`)
}
