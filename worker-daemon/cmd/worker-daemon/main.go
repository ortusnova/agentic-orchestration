// Command worker-daemon runs a worker host's jobs for the control plane.
// It dials the control plane's worker API (protocol 1), runs each job
// it takes in a smolvm machine, and carries the job's data-plane flows.
//
// Usage:
//
//	worker-daemon version
//	worker-daemon run -config FILE
//	worker-daemon check -config FILE
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/config"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/protocol"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: worker-daemon version | run -config FILE | check -config FILE")
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "version", "-version", "--version":
		printVersion(stdout)
		return 0
	case "run", "check":
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		fs.SetOutput(stderr)
		path := fs.String("config", "/etc/worker-daemon/config.json", "configuration file")
		if err := fs.Parse(args[1:]); err != nil {
			return 2
		}
		cfg, err := config.Load(*path)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if args[0] == "check" {
			fmt.Fprintf(stdout, "%s: ok\n", *path)
			return 0
		}
		return serve(cfg, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	}
	usage(stderr)
	return 2
}

func printVersion(w io.Writer) {
	vs := make([]string, len(protocol.Versions))
	for i, v := range protocol.Versions {
		vs[i] = strconv.Itoa(v)
	}
	fmt.Fprintf(w, "worker-daemon %s\nworker API protocol %s\n", version, strings.Join(vs, ", "))
}

func serve(cfg config.Config, stderr io.Writer) int {
	var level slog.Level
	_ = level.UnmarshalText([]byte(cfg.LogLevel))
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))
	d, err := daemon.New(daemon.Options{
		Config:  cfg,
		Probe:   hostinfo.Host{},
		Backend: &machine.Smolvm{Binary: cfg.Smolvm.Binary},
		Log:     log.With(slog.String("version", version)),
	})
	if err != nil {
		log.Error("cannot start", slog.String("error", err.Error()))
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := d.Run(ctx); err != nil {
		log.Error("stopped", slog.String("error", err.Error()))
		return 1
	}
	return 0
}
