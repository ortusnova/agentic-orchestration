// Command flowprobe drives a job's flows with the worker daemon's own
// clients, for the control plane's live test (control-plane/livetest):
// what a worker host sends through Pomerium is then what the daemon
// sends, not a request the test writes by hand.
//
//	flowprobe -offer FILE -cert C -key K -ca CA stdio
//	    open the stdio stream with flows.Job.DialStdio; print the
//	    protocol it agreed ("sortie-stdio/2"), then carry frames between
//	    it and stdin and stdout until either ends
//	flowprobe -offer FILE -cert C -key K -ca CA connect HOST:PORT
//	    send CONNECT HOST:PORT through the daemon's egress forwarder
//	    (flows.Egress), as a guest does, and print the status it got
//
// A refused stdio upgrade prints the error on stderr and exits 3.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/config"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/cp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/flows"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/stdio"
)

func main() {
	offerFile := flag.String("offer", "", "the poll's answer, as JSON")
	var t config.TLS
	flag.StringVar(&t.CertFile, "cert", "", "the worker's client certificate")
	flag.StringVar(&t.KeyFile, "key", "", "its key")
	flag.StringVar(&t.CAFile, "ca", "", "the CA of the control plane's certificate")
	flag.StringVar(&t.ServerName, "server-name", "", "the control plane's TLS name, when not the URL's host")
	flag.Parse()
	if err := run(*offerFile, t, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "flowprobe:", err)
		if _, ok := errors.AsType[*stdio.RefusedError](err); ok {
			os.Exit(3)
		}
		os.Exit(1)
	}
}

func run(offerFile string, t config.TLS, args []string) error {
	b, err := os.ReadFile(offerFile) // #nosec G304 -- the test names the file
	if err != nil {
		return err
	}
	var o protocol.Offer
	if err := json.Unmarshal(b, &o); err != nil {
		return fmt.Errorf("offer: %w", err)
	}
	tlsConfig, err := cp.TLSConfig(t)
	if err != nil {
		return err
	}
	// One configuration for the control-plane client and the flows, as
	// in the daemon.
	j, err := flows.New(&o, cp.HTTPClient(tlsConfig), tlsConfig)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// By the time a job runs, the daemon's control-plane client has
	// made its requests on that configuration: make one too.
	if err := warm(ctx, j); err != nil {
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "stdio":
		return carry(ctx, j)
	case len(args) == 2 && args[0] == "connect":
		return connect(ctx, j, args[1])
	}
	return errors.New("usage: flowprobe -offer FILE [-cert C -key K -ca CA] stdio | connect HOST:PORT")
}

func warm(ctx context.Context, j *flows.Job) error {
	u := j.Flows[protocol.FlowUsage].URL
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Scheme+"://"+u.Host+"/", nil)
	if err != nil {
		return err
	}
	resp, err := j.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("a control-plane request: %w", err)
	}
	_ = resp.Body.Close()
	return nil
}

func carry(ctx context.Context, j *flows.Job) error {
	c, err := j.DialStdio(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	proto := stdio.Protocol
	if c.Files() {
		proto = stdio.ProtocolFiles
	}
	out := bufio.NewWriter(os.Stdout)
	if _, err := fmt.Fprintln(out, proto); err != nil {
		return err
	}
	if err := out.Flush(); err != nil {
		return err
	}
	go func() {
		in := bufio.NewReader(os.Stdin)
		for {
			typ, payload, err := stdio.ReadFrame(in)
			if err != nil {
				_ = c.Close()
				return
			}
			if c.WriteFrame(typ, payload) != nil {
				return
			}
		}
	}()
	for {
		typ, payload, err := c.ReadFrame()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		if err := stdio.WriteFrame(out, typ, payload); err != nil {
			return err
		}
		if err := out.Flush(); err != nil {
			return err
		}
	}
}

func connect(ctx context.Context, j *flows.Job, host string) error {
	a := j.Flows[protocol.FlowEgress]
	e := &flows.Egress{Upstream: a.URL, Token: a.Token, TLS: j.TLS, Job: j.ID}
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: e, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", host, host); err != nil {
		return err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	fmt.Println(resp.StatusCode)
	return nil
}
