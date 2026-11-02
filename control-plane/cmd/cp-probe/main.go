// cp-probe checks that a service answers, for the compose project's
// health checks: an http(s) URL must answer 2xx, a tcp://host:port must
// accept a connection. Images without a shell or curl carry it.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/egress"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "cp-probe:", err)
		os.Exit(1)
	}
}

func run() error {
	bearerFile := flag.String("bearer-file", "", "send the token in this file as Authorization: Bearer")
	print := flag.Bool("print", false, "print the answer's body")
	timeout := flag.Duration("timeout", 5*time.Second, "give up after this long")
	flag.Parse()
	if flag.NArg() != 1 {
		return fmt.Errorf("usage: cp-probe [-bearer-file FILE] [-print] URL|tcp://HOST:PORT")
	}
	u, err := url.Parse(flag.Arg(0))
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if u.Scheme == "tcp" {
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
		if err != nil {
			return err
		}
		return c.Close()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if *bearerFile != "" {
		tok, err := egress.ReadSecret(*bearerFile)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if *print {
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			return err
		}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s answered %s", u.Redacted(), resp.Status)
	}
	return nil
}
