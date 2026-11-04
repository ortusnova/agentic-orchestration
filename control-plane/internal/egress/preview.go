package egress

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Previews are the previews of review: ui stories' pull requests on the
// gate's host (agent-platform/run-supervision, "A UI is approved as a
// prototype, and the built story merges after its review"). The review of
// such a story runs in a job's machine on any worker, and opens its
// preview at an http:// name under Domain: the worker's egress forwarder
// sends a CONNECT for that name's port 80 on the egress flow, through
// Pomerium, as for any other host. The gate admits it by the job's live
// egress token, never sends it to nono, and serves it from the preview's
// port on its own loopback, which is never opened on the host's network.
type Previews struct {
	// Domain is the previews' domain: each preview is a name under it,
	// such as ons-93.preview.p1.example.org under preview.p1.example.org.
	Domain string
	// RoutesFile lists each deployed preview, one "NAME PORT" line, as
	// the kit's deploy writes it. It is read on each CONNECT; a missing
	// file lists none.
	RoutesFile string
	// ReviewKeyFile, when set, holds the key the gate gives every request
	// of a job to a preview, in ReviewKeyHeader, beside the job in
	// ReviewJobHeader: a preview that holds the same key signs the review
	// in by it. A request's own values of both headers are dropped.
	ReviewKeyFile string
}

const (
	// ReviewKeyHeader carries the previews' review key to a preview.
	ReviewKeyHeader = "X-Workbench-Review-Key"
	// ReviewJobHeader names the job whose request it is, as Sortie's
	// introspection gave it.
	ReviewJobHeader = "X-Workbench-Review-Job"
	// JobHeader is the job a worker's egress forwarder names on each
	// CONNECT. It is a claim, logged beside a refusal only: the gate
	// admits by the job token.
	JobHeader = "X-Sortie-Job"
)

// PreviewPort is the only port a preview's name answers on: its address
// is http://NAME/, which a guest's browser reaches through its HTTP proxy.
const PreviewPort = "80"

func (p *Previews) validate() error {
	d := strings.ToLower(strings.Trim(p.Domain, "."))
	if d == "" || strings.ContainsAny(d, " /:*") || !strings.Contains(d, ".") {
		return fmt.Errorf("previews: %q is not a domain", p.Domain)
	}
	p.Domain = d
	if p.RoutesFile == "" {
		return errors.New("previews: the routes file is missing")
	}
	return nil
}

// isPreview reports whether host:port names a preview: a name under
// the previews' domain, whatever its port.
func (g *Gate) isPreview(hostport string) bool {
	if g.cfg.Previews == nil {
		return false
	}
	host := strings.ToLower(hostport)
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = strings.ToLower(h)
	}
	return strings.HasSuffix(host, "."+g.cfg.Previews.Domain)
}

// previewPort is the loopback port the routes file gives name.
func (g *Gate) previewPort(name string) (int, bool) {
	b, err := os.ReadFile(g.cfg.Previews.RoutesFile) // #nosec G304 -- the configuration names the file
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			g.log.Warn("previews unreadable", slog.String("error", err.Error()))
		}
		return 0, false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !strings.EqualFold(f[0], name) {
			continue
		}
		port, err := strconv.Atoi(f[1])
		if err != nil || port <= 0 || port > 65535 {
			return 0, false
		}
		return port, true
	}
	return 0, false
}

// preview serves a CONNECT for a preview's name. A job's live egress
// token admits it; so does Sortie's control token, which only the kit's
// check that a preview answers holds (workbench preview-sync), and which
// gets no review key. Every refusal is logged with the worker, the job the
// worker claims and the host.
func (g *Gate) preview(w http.ResponseWriter, r *http.Request, worker string) {
	host := r.Host
	claimed := r.Header.Get(JobHeader)
	refuse := func(status int, reason, msg string) {
		g.log.Info("preview refused", slog.String("worker", worker), slog.String("job", claimed), slog.String("host", host),
			slog.Int("status", status), slog.String("reason", reason))
		if status == http.StatusProxyAuthRequired {
			w.Header().Set("Proxy-Authenticate", `Bearer realm="sortie-job"`)
		}
		http.Error(w, msg, status)
	}
	token := proxyToken(r)
	var grant Grant
	check := token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(g.controlToken)) == 1
	if !check {
		var ok bool
		var err error
		grant, ok, err = g.admit(r.Context(), token)
		if err != nil {
			unavailable(w)
			return
		}
		if !ok {
			refuse(http.StatusProxyAuthRequired, "no live egress token", "the job token does not admit the egress flow")
			return
		}
		claimed = grant.Job
	}
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = host, PreviewPort
	}
	if port != PreviewPort {
		refuse(http.StatusForbidden, "a preview answers on port 80 only", "a preview's address is http://"+name+"/")
		return
	}
	local, ok := g.previewPort(name)
	if !ok {
		refuse(http.StatusNotFound, "no preview at this name", "no preview is deployed at "+name)
		return
	}
	attrs := []any{slog.String("worker", worker), slog.String("job", grant.Job), slog.Uint64("attempt", grant.Attempt),
		slog.String("host", host), slog.Int("port", local)}
	if check {
		attrs = append(attrs, slog.Bool("check", true))
	}
	key := ""
	if !check && g.cfg.Previews.ReviewKeyFile != "" {
		if key, err = ReadSecret(g.cfg.Previews.ReviewKeyFile); err != nil {
			g.log.Warn("preview review key unreadable", slog.String("error", err.Error()))
		}
	}
	client, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		http.Error(w, "the connection cannot be tunnelled", http.StatusInternalServerError)
		return
	}
	g.log.Info("preview tunnel", attrs...)
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		return
	}
	target := &url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(local))}
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// The preview sees the name it was asked for.
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del(ReviewKeyHeader)
			pr.Out.Header.Del(ReviewJobHeader)
			pr.Out.Header.Del("Proxy-Authorization")
			if key != "" {
				pr.Out.Header.Set(ReviewKeyHeader, key)
				pr.Out.Header.Set(ReviewJobHeader, grant.Job)
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			g.log.Warn("preview unreachable", append(attrs, slog.String("error", err.Error()))...)
			http.Error(w, "the preview does not answer", http.StatusBadGateway)
		},
	}
	logged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		rp.ServeHTTP(rec, r)
		g.log.Info("preview request", append(attrs, slog.String("method", r.Method), slog.String("path", r.URL.Path),
			slog.Int("status", rec.status))...)
	})
	serveConn(&bufConn{Conn: client, r: buf.Reader}, logged)
}

// statusRecorder keeps the status a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets the response controller reach the connection, for an
// upgrade such as a WebSocket.
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// bufConn reads what the CONNECT's reader already buffered first.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// serveConn serves HTTP on one connection until it closes.
func serveConn(c net.Conn, h http.Handler) {
	l := &oneConn{conn: c, done: make(chan struct{})}
	srv := &http.Server{
		Handler: h, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute,
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed || s == http.StateHijacked {
				l.close()
			}
		},
	}
	_ = srv.Serve(l)
}

// oneConn is a listener that accepts one connection, then waits until
// that connection ends.
type oneConn struct {
	mu   sync.Mutex
	conn net.Conn
	done chan struct{}
	once sync.Once
}

func (l *oneConn) Accept() (net.Conn, error) {
	l.mu.Lock()
	c := l.conn
	l.conn = nil
	l.mu.Unlock()
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConn) close() { l.once.Do(func() { close(l.done) }) }

func (l *oneConn) Close() error { l.close(); return nil }

func (l *oneConn) Addr() net.Addr { return previewAddr{} }

type previewAddr struct{}

func (previewAddr) Network() string { return "tcp" }
func (previewAddr) String() string  { return "preview" }
