// cp-egress is the egress gate behind Pomerium's egress route
// (internal/egress).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/egress"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cp-egress:", err)
		os.Exit(1)
	}
}

func run() error {
	listen := flag.String("listen", ":7691", "where the gate listens, on the internal network")
	var cfg egress.Config
	flag.StringVar(&cfg.Sortie, "sortie", "http://sortie:7680", "Sortie's base URL")
	flag.StringVar(&cfg.ControlTokenFile, "control-token-file", "/run/secrets/sortie-control-token", "Sortie's control token")
	flag.StringVar(&cfg.IdentityHeader, "identity-header", egress.DefaultIdentityHeader, "the header Pomerium names the worker in")
	flag.StringVar(&cfg.Nono, "nono", "nono:7690", "nono's proxy, host:port")
	flag.StringVar(&cfg.NonoPassFile, "nono-pass-file", "/run/secrets/nono-pass", "the credential nono's proxy takes")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	g, err := egress.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: *listen, Handler: g, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shut)
	}()
	log.Info("egress gate listening", slog.String("addr", *listen), slog.String("nono", cfg.Nono))
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
