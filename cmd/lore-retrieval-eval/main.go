package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"lore/internal/retrievaleval"
	"lore/internal/version"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("lore-retrieval-eval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "eval/retrieval/suite.yaml", "retrieval suite YAML path")
	baselinePath := flags.String("baseline", "", "baseline JSON path (defaults beside suite.yaml)")
	jsonOutput := flags.Bool("json", false, "write the complete current report as JSON")
	checkBaseline := flags.Bool("check-baseline", true, "compare the current report with the checked-in baseline")
	writeBaseline := flags.Bool("write-baseline", false, "replace the baseline with the current report")
	flags.Usage = func() {
		fmt.Fprintln(stderr, "Usage: go run ./cmd/lore-retrieval-eval [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "lore-retrieval-eval does not accept positional arguments")
		return 2
	}
	if *writeBaseline && !*checkBaseline {
		fmt.Fprintln(stderr, "--write-baseline cannot be combined with --check-baseline=false")
		return 2
	}
	if *baselinePath == "" {
		*baselinePath = filepath.Join(filepath.Dir(*suitePath), "baseline.json")
	}

	report, err := retrievaleval.Run(ctx, retrievaleval.RunOptions{SuitePath: *suitePath, LoreVersion: version.Version})
	if err != nil {
		fmt.Fprintf(stderr, "retrieval evaluation failed: %v\n", err)
		return 1
	}
	if *jsonOutput {
		if err := retrievaleval.WriteJSON(stdout, report); err != nil {
			fmt.Fprintf(stderr, "write retrieval report: %v\n", err)
			return 1
		}
	} else {
		retrievaleval.RenderText(stdout, report)
	}
	if retrievaleval.HasAuthorizationFailure(report) {
		fmt.Fprintln(stderr, "retrieval evaluation failed: an authorization expectation was violated")
		return 1
	}
	if *writeBaseline {
		if err := retrievaleval.WriteReport(*baselinePath, report); err != nil {
			fmt.Fprintf(stderr, "write retrieval baseline: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "wrote retrieval baseline %s\n", *baselinePath)
		return 0
	}
	if !*checkBaseline {
		return 0
	}
	baseline, err := retrievaleval.LoadReport(*baselinePath)
	if err != nil {
		fmt.Fprintf(stderr, "check retrieval baseline: %v\n", err)
		return 1
	}
	differences := retrievaleval.DiffReports(baseline, report)
	if len(differences) == 0 {
		fmt.Fprintln(stderr, "retrieval baseline matches")
		return 0
	}
	fmt.Fprintln(stderr, "retrieval baseline differs:")
	for _, difference := range differences {
		fmt.Fprintf(stderr, "  - %s\n", difference)
	}
	fmt.Fprintln(stderr, "review the changes, then use --write-baseline only when the new behavior is intentional")
	return 1
}
