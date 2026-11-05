package flows

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
)

// Egress is the guest's HTTP proxy. It carries each CONNECT tunnel to
// the egress flow, itself an HTTP proxy, adding the job's egress token as
// Proxy-Authorization and the job's identifier as JobHeader. A plain
// proxied request, such as a browser's for an http:// address, goes the
// same way: the forwarder opens a CONNECT tunnel to its host, port 80 by
// default, on the egress flow and sends the request inside it, so the
// control plane's front sees a CONNECT for every request the guest makes.
// A guest's own Proxy-Authorization is dropped. Requests counts what it
// carried, so the daemon knows whether a run made model requests.
type Egress struct {
	Upstream *url.URL
	Token    secret.Token
	TLS      *tls.Config
	// Job is the job's identifier, which the forwarder names on each
	// CONNECT so the egress gate can log the job of a refused token.
	Job string

	requests  atomic.Int64
	once      sync.Once
	transport *http.Transport
}

// Requests is how many requests the forwarder carried.
func (e *Egress) Requests() int64 { return e.requests.Load() }

func (e *Egress) proxyAuth() string {
	if e.Token.Empty() {
		return ""
	}
	return "Bearer " + e.Token.Reveal()
}

func (e *Egress) upstreamAddr() (string, bool) {
	tlsUp := e.Upstream.Scheme == "https" || e.Upstream.Scheme == "wss"
	if e.Upstream.Port() != "" {
		return e.Upstream.Host, tlsUp
	}
	if tlsUp {
		return net.JoinHostPort(e.Upstream.Hostname(), "443"), true
	}
	return net.JoinHostPort(e.Upstream.Hostname(), "80"), false
}

func (e *Egress) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		e.connect(w, r)
		return
	}
	if !r.URL.IsAbs() {
		http.Error(w, "this is an HTTP proxy: send absolute-form requests or CONNECT", http.StatusBadRequest)
		return
	}
	e.once.Do(func() {
		e.transport = &http.Transport{
			DialContext:           e.dialTunnel,
			ResponseHeaderTimeout: 10 * time.Minute,
			MaxIdleConnsPerHost:   4,
			IdleConnTimeout:       90 * time.Second,
		}
	})
	e.requests.Add(1)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = pr.In.URL
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Proxy-Authorization")
		},
		Transport:     e.transport,
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			if refused, ok := errors.AsType[*tunnelRefused](err); ok {
				w.WriteHeader(refused.status)
				_, _ = w.Write(refused.body)
				return
			}
			http.Error(w, "egress flow: "+err.Error(), http.StatusBadGateway)
		},
	}
	r, cancel := detached(r)
	defer cancel()
	rp.ServeHTTP(w, r)
}

// detached is r with a context that the guest's half-close does not
// cancel. net/http cancels a request's context when it reads EOF from
// the client, and a guest that writes its request and then closes its
// write side, as `printf ... | socat - TCP:...` does, still waits for
// the answer: the upstream request ends when the upstream answers, or
// when writing that answer to a guest that is gone fails. The context
// has a Done channel, closed by the returned cancel, so that a
// ReverseProxy does not watch CloseNotify, which the same EOF fires.
func detached(r *http.Request) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	return r.WithContext(ctx), cancel
}

// tunnelRefused is the egress flow's answer to a CONNECT other than 200.
type tunnelRefused struct {
	status int
	body   []byte
}

func (t *tunnelRefused) Error() string { return fmt.Sprintf("the egress flow answered %d", t.status) }

// dialTunnel is the plain requests' dialer: a CONNECT tunnel to addr on
// the egress flow.
func (e *Egress) dialTunnel(ctx context.Context, _, addr string) (net.Conn, error) {
	conn, br, resp, err := e.tunnel(ctx, addr)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = conn.Close() }()
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, &tunnelRefused{status: resp.StatusCode, body: body}
	}
	_ = resp.Body.Close()
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn reads what the CONNECT answer's reader already buffered first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// tunnel sends a CONNECT for addr on the egress flow and returns the
// connection, its reader and the flow's answer.
func (e *Egress) tunnel(ctx context.Context, addr string) (net.Conn, *bufio.Reader, *http.Response, error) {
	upAddr, useTLS := e.upstreamAddr()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var d net.Dialer
	up, err := d.DialContext(ctx, "tcp", upAddr)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("egress flow unreachable: %w", err)
	}
	if useTLS {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if e.TLS != nil {
			cfg = e.TLS.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = e.Upstream.Hostname()
		}
		// The CONNECT is written as HTTP/1.1: a configuration that also
		// offers h2 would let the front agree h2 and drop it.
		cfg.NextProtos = []string{"http/1.1"}
		tc := tls.Client(up, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = up.Close()
			return nil, nil, nil, fmt.Errorf("egress flow TLS failed: %w", err)
		}
		up = tc
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: addr}, Host: addr, Header: http.Header{}}
	if a := e.proxyAuth(); a != "" {
		req.Header.Set("Proxy-Authorization", a)
	}
	req.Header.Set(protocol.ProtocolHeader, strconv.Itoa(protocol.Version))
	if e.Job != "" {
		req.Header.Set(protocol.JobHeader, e.Job)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = up.SetDeadline(dl)
	}
	if err := req.Write(up); err != nil {
		_ = up.Close()
		return nil, nil, nil, fmt.Errorf("egress flow write failed: %w", err)
	}
	br := bufio.NewReader(up)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = up.Close()
		return nil, nil, nil, fmt.Errorf("egress flow answered nothing: %w", err)
	}
	_ = up.SetDeadline(time.Time{})
	return up, br, resp, nil
}

func (e *Egress) connect(w http.ResponseWriter, r *http.Request) {
	e.requests.Add(1)
	// Not r's context: the guest's half-close cancels it (see detached).
	up, br, resp, err := e.tunnel(context.WithoutCancel(r.Context()), r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = up.Close() }()
		defer func() { _ = resp.Body.Close() }()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<10))
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()
		http.Error(w, "cannot tunnel", http.StatusInternalServerError)
		return
	}
	client, crw, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = up.Close()
		return
	}
	splice(client, crw.Reader, up, br)
}

// splice copies both ways until either side ends, including what each
// reader had already buffered.
func splice(a net.Conn, ar io.Reader, b net.Conn, br io.Reader) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(b, ar); closeWrite(b); done <- struct{}{} }()
	go func() { _, _ = io.Copy(a, br); closeWrite(a); done <- struct{}{} }()
	<-done
	<-done
	_ = a.Close()
	_ = b.Close()
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// MCP carries the guest's tool calls to the mcp flow: a request for
// /p reaches {mcp}/jobs/{job}/attempts/{n}/p with the job's mcp token.
// A guest's own Authorization is dropped.
type MCP struct {
	Job   *Job
	calls atomic.Int64
	once  sync.Once
	rp    *httputil.ReverseProxy
}

// Calls is how many requests the forwarder carried.
func (m *MCP) Calls() int64 { return m.calls.Load() }

func (m *MCP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.once.Do(func() {
		a := m.Job.Flows[protocol.FlowMCP]
		base := Join(httpScheme(a.URL), protocol.AttemptSuffix(m.Job.ID, m.Job.Attempt))
		m.rp = &httputil.ReverseProxy{
			Rewrite: func(pr *httputil.ProxyRequest) {
				u := Join(base, pr.In.URL.Path)
				u.RawQuery = pr.In.URL.RawQuery
				pr.Out.URL = u
				pr.Out.Host = u.Host
				pr.Out.Header.Del("Authorization")
				if !a.Token.Empty() {
					pr.Out.Header.Set("Authorization", "Bearer "+a.Token.Reveal())
				}
				pr.Out.Header.Set(protocol.ProtocolHeader, strconv.Itoa(protocol.Version))
			},
			Transport:     m.Job.HTTP.Transport,
			FlushInterval: -1,
		}
	})
	m.calls.Add(1)
	r, cancel := detached(r)
	defer cancel()
	m.rp.ServeHTTP(w, r)
}

// Server serves one forwarder on a Unix socket, which only the job's
// machine is given.
type Server struct {
	Path string
	ln   net.Listener
	srv  *http.Server
}

// Serve listens on a Unix socket at path, readable only by this user.
func Serve(path string, h http.Handler) (*Server, error) {
	_ = os.Remove(path)
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "unix", path)
	if err != nil {
		return nil, fmt.Errorf("flows: listen %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	s := &Server{Path: path, ln: ln, srv: &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second}}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr is the listener's address.
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Close stops the server and removes its socket.
func (s *Server) Close() error {
	err := s.srv.Close()
	_ = os.Remove(s.Path)
	return err
}
