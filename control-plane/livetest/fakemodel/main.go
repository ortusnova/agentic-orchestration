// fakemodel stands in for a model API in the control plane's live tests:
// an HTTPS server that answers every request with what it received, the
// Authorization header included, so a test sees the credential nono put
// in place of the placeholder. With -issue DIR it writes a CA and a
// certificate for -name signed by it, and exits.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/pki"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakemodel:", err)
		os.Exit(1)
	}
}

func run() error {
	issue := flag.String("issue", "", "write ca.crt, model.crt and model.key into this directory and exit")
	name := flag.String("name", "model.cp.test", "the host name of the certificate")
	listen := flag.String("listen", ":443", "where to serve")
	cert := flag.String("cert", "/etc/fakemodel/model.crt", "the server certificate")
	key := flag.String("key", "/etc/fakemodel/model.key", "the server key")
	flag.Parse()
	if *issue != "" {
		ca, err := pki.NewCA("live-test model CA")
		if err != nil {
			return err
		}
		leaf, err := ca.IssueServer([]string{*name})
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(*issue, "ca.crt"), pki.CertPEM(ca.Cert), 0o644); err != nil { // #nosec G306 -- a test CA's certificate
			return err
		}
		if err := pki.WritePair(filepath.Join(*issue, "model.crt"), filepath.Join(*issue, "model.key"), leaf.Cert, leaf.Key); err != nil {
			return err
		}
		// The container's user reads the key through a bind mount.
		return os.Chmod(filepath.Join(*issue, "model.key"), 0o644) // #nosec G302 -- a throwaway test key
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Info("model request", slog.String("method", r.Method), slog.String("host", r.Host), slog.String("path", r.URL.Path))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method, "host": r.Host, "path": r.URL.Path, "authorization": r.Header.Get("Authorization"),
		})
	})
	srv := &http.Server{Addr: *listen, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	log.Info("fake model listening", slog.String("addr", *listen))
	return srv.ListenAndServeTLS(*cert, *key)
}
