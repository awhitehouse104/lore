package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"lore/internal/core"
	"lore/internal/output"
	"lore/internal/version"
)

type streams struct {
	in  io.Reader
	out io.Writer
	err io.Writer
}

func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = ctx
	s := streams{in: stdin, out: stdout, err: stderr}
	if len(args) == 0 {
		printRootUsage(stderr)
		return core.ExitUsage
	}

	switch args[0] {
	case "version":
		return runVersion(args[1:], s)
	case "help", "--help", "-h":
		printRootUsage(stdout)
		return core.ExitOK
	default:
		fmt.Fprintf(stderr, "lore: unknown command %q\n", args[0])
		printRootUsage(stderr)
		return core.ExitUsage
	}
}

func runVersion(args []string, s streams) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(s.err)
	jsonOutput := fs.Bool("json", false, "emit JSON")
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
		fmt.Fprintln(s.err, "lore version: unexpected positional arguments")
		return core.ExitUsage
	}

	info := version.Current()
	if *jsonOutput {
		if err := output.JSON(s.out, info); err != nil {
			fmt.Fprintf(s.err, "lore version: write output: %v\n", err)
			return core.ExitRuntime
		}
		return core.ExitOK
	}
	fmt.Fprintf(s.out, "lore %s (%s, %s)\n", info.Version, info.Commit, info.BuildDate)
	return core.ExitOK
}

func printRootUsage(w io.Writer) {
	fmt.Fprintln(w, `Lore is a deterministic Markdown-and-Git knowledge repository CLI.

Usage:
  lore <command> [options]

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
