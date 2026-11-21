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
	// An attempt's nono runs this for each GitHub request (egress.ForgeTokens).
	if len(os.Args) > 1 && os.Args[1] == egress.CredentialCommand {
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: cp-egress", egress.CredentialCommand, "DIR bearer|basic")
			os.Exit(2)
		}
		if err := egress.Credential(os.Args[2], os.Args[3], os.Getenv("NONO_CACHE_SCOPE"), os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "cp-egress:", err)
			os.Exit(1)
		}
		return
	}
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
	var lp egress.LiveProof
	flag.StringVar(&lp.Nono, "live-proof-nono", "", "the live-proof nono's proxy, host:port; empty: no live-proof egress")
	flag.StringVar(&lp.JobsFile, "live-proof-jobs-file", "", "the file of the live-proof jobs that run, one per line")
	var ft egress.ForgeTokens
	flag.StringVar(&ft.Nono, "forge-nono", "", "the nono binary the gate runs for each attempt that holds GitHub tokens; empty: Sortie cannot register any")
	flag.StringVar(&ft.CACert, "forge-ca-cert", "", "the CA certificate an attempt's nono intercepts TLS with: the shared nono's")
	flag.StringVar(&ft.CAKey, "forge-ca-key", "", "the CA's key")
	flag.StringVar(&ft.Dir, "forge-dir", "", "the directory of the registered attempts' token files, best on a tmpfs")
	var pv egress.Previews
	flag.StringVar(&pv.Domain, "preview-domain", "", "the previews' domain: a CONNECT for a name under it is served from the preview's loopback port; empty: no previews")
	flag.StringVar(&pv.RoutesFile, "previews-file", "", "the deployed previews, one \"NAME PORT\" line each")
	flag.StringVar(&pv.ReviewKeyFile, "preview-review-key-file", "", "the key the gate gives a job's requests to a preview, which signs its review in")
	flag.Parse()
	if lp.Nono != "" || lp.JobsFile != "" {
		cfg.LiveProof = &lp
	}
	if pv.Domain != "" || pv.RoutesFile != "" || pv.ReviewKeyFile != "" {
		cfg.Previews = &pv
	}
	if ft.Nono != "" || ft.CACert != "" || ft.CAKey != "" || ft.Dir != "" {
		cfg.ForgeTokens = &ft
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	g, err := egress.New(cfg, log)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ran := make(chan struct{})
	go func() { g.Run(ctx); close(ran) }()
	defer func() { stop(); <-ran }()
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
