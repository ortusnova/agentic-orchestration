// Command smol-builder turns a repository's smol-builder.toml into a smolvm
// pack.
//
//	smol-builder build --repo <dir> --output <file> [--layers <dir>]
//
// On success it prints one JSON report to standard output. Diagnostics go
// to standard error.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/build"
)

const usage = "usage: smol-builder build --repo <dir> --output <file> [--layers <dir>]"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr, build.New(os.Stderr))
	stop()
	os.Exit(code)
}

// packBuilder is the build step run invokes; tests replace it.
type packBuilder interface {
	Build(ctx context.Context, opts build.Options) (*build.Report, error)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, b packBuilder) int {
	if len(args) == 0 || args[0] != "build" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, usage)
		fs.PrintDefaults()
	}
	var opts build.Options
	fs.StringVar(&opts.Repo, "repo", "", "repository root holding smol-builder.toml")
	fs.StringVar(&opts.Output, "output", "", "path to write the pack to")
	fs.StringVar(&opts.LayersDir, "layers", "", "layer library directory (default: the shipped library)")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if opts.Repo == "" || opts.Output == "" || fs.NArg() > 0 {
		fs.Usage()
		return 2
	}

	report, err := b.Build(ctx, opts)
	if err != nil {
		fmt.Fprintf(stderr, "smol-builder: %v\n", err)
		return 1
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		fmt.Fprintf(stderr, "smol-builder: %v\n", err)
		return 1
	}
	return 0
}
