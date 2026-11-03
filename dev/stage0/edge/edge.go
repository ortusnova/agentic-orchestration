// The stage-1 edge of the stage-0 kit (spec store agent-platform-workers,
// design D18). It runs on p1 and is the only way a worker host reaches the
// control plane: p1 opens one SSH tunnel to each worker host, and each
// tunnel ends on a loopback port of its own here, so the port a request
// arrives on names the worker. The edge sets the worker identity header
// from that port, routes the worker API to Sortie and each data-plane flow
// to the service that serves it, and admits a flow request only when
// Sortie's token introspection says its job token is live and granted for
// that flow. The egress flow is the CONNECT proxy on the same port; its
// service is nono's proxy, which holds the credentials. A live-proof job
// that runs gets a second nono's, of the live-proof profile, instead,
// and a job of a repository with a profile of its own (homelab) gets the
// nono of that profile.
//
// Pomerium and the CA in control-plane-images replace it later.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Flows the edge proxies by path, to the service the configuration names.
// The fifth flow, egress, is the CONNECT proxy.
var pathFlows = []string{"stdio", "mcp", "bundles", "usage"}

const (
	egressFlow = "egress"

	// workerAPIPrefix is the path every route of Sortie's worker API sits under.
	workerAPIPrefix = "/api/v1/workers/"

	// DefaultIdentityHeader is Sortie's default worker.identity_header.
	DefaultIdentityHeader = "X-Sortie-Worker-Identity"

	introspectPath = "/api/v1/tokens/introspect"
)

var workerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Worker is one worker host: the identity Sortie knows it by, and the
// loopback port its tunnel ends on.
type Worker struct {
	Name string `json:"name"`
	Port int    `json:"port"`
}

// Config is the edge's configuration file.
type Config struct {
	// Sortie is the base URL of Sortie's HTTP server.
	Sortie string `json:"sortie"`
	// ControlTokenFile holds Sortie's control token, which introspection needs.
	ControlTokenFile string `json:"control_token_file"`
	// IdentityHeader is Sortie's worker.identity_header.
	IdentityHeader string `json:"identity_header"`
	// Workers lists each worker host and its port.
	Workers []Worker `json:"workers"`
	// Flows maps stdio, mcp, bundles and usage to the URL of the service
	// that serves each. A request to /<flow>/rest reaches <url>/rest.
	Flows map[string]string `json:"flows"`
	// Egress is the host:port of nono's proxy, on loopback.
	Egress string `json:"egress"`
	// EgressPassFile holds the credential nono's proxy takes.
	EgressPassFile string `json:"egress_pass_file"`
	// LiveProof, when set, is the egress of the live-proof profile: the
	// jobs its file names reach the hosts of the second nono's profile.
	LiveProof *LiveProof `json:"live_proof,omitempty"`
	// Repositories maps a repository Sortie dispatches to the host:port
	// of the nono proxy of that repository's own profile, on loopback: a
	// job of the repository reaches only the hosts of that profile, as
	// homelab's reach only those homelab declares. It takes the same
	// credential as Egress. A job of any other repository gets Egress.
	Repositories map[string]string `json:"repositories,omitempty"`
}

// LiveProof is the egress of live-proof jobs (agent-platform/run-supervision,
// "A live criterion a story's machine cannot run becomes a live-proof job").
type LiveProof struct {
	// Egress is the host:port of the nono proxy that runs the live-proof
	// profile, on loopback. It takes the same credential as Egress.
	Egress string `json:"egress"`
	// JobsFile lists the job of each live proof that runs, one issue
	// identifier per line, as stage0-live-proofs writes it. A missing file
	// lists none.
	JobsFile string `json:"jobs_file"`
}

// LoadConfig reads and checks a configuration file.
func LoadConfig(file string) (Config, error) {
	var cfg Config
	b, err := os.ReadFile(file) // #nosec G304 -- the operator names the file
	if err != nil {
		return cfg, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("%s: %w", file, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("%s: %w", file, err)
	}
	return cfg, nil
}

// Validate checks that each worker has a name and a port of its own, and
// that every flow has a service.
func (c Config) Validate() error {
	names := map[string]bool{}
	ports := map[int]string{}
	for _, w := range c.Workers {
		if !workerName.MatchString(w.Name) {
			return fmt.Errorf("worker name %q: want letters, digits, '.', '_' or '-'", w.Name)
		}
		if names[w.Name] {
			return fmt.Errorf("worker %s is listed twice", w.Name)
		}
		names[w.Name] = true
		if w.Port < 1 || w.Port > 65535 {
			return fmt.Errorf("worker %s: port %d is out of range", w.Name, w.Port)
		}
		if other, ok := ports[w.Port]; ok {
			return fmt.Errorf("workers %s and %s share port %d; each tunnel needs its own", other, w.Name, w.Port)
		}
		ports[w.Port] = w.Name
	}
	if lp := c.LiveProof; lp != nil {
		if lp.Egress == "" || lp.Egress == c.Egress {
			return errors.New("live_proof.egress: want the live-proof nono's address, apart from egress")
		}
		if lp.JobsFile == "" {
			return errors.New("live_proof.jobs_file is missing")
		}
	}
	for repo, addr := range c.Repositories {
		if !workerName.MatchString(repo) {
			return fmt.Errorf("repositories: %q is not a repository name", repo)
		}
		if addr == "" || addr == c.Egress || (c.LiveProof != nil && addr == c.LiveProof.Egress) {
			return fmt.Errorf("repositories.%s: want the address of its own nono, apart from egress and live_proof.egress", repo)
		}
	}
	return nil
}

// Grant is what Sortie's introspection says a live token grants.
type Grant struct {
	Job     string `json:"job"`
	Attempt uint64 `json:"attempt"`
	Flow    string `json:"flow"`
}

// Edge serves every worker's port.
type Edge struct {
	cfg            Config
	sortie         *url.URL
	controlToken   string
	identityHeader string
	egressAuth     string
	client         *http.Client
	log            *slog.Logger
	toSortie       http.Handler
	toFlow         map[string]http.Handler

	reposMu sync.Mutex
	repos   map[string]string // job#attempt -> its repository
}

// New reads the token files and builds the edge.
func New(cfg Config, log *slog.Logger) (*Edge, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	sortie, err := serviceURL("sortie", cfg.Sortie)
	if err != nil {
		return nil, err
	}
	control, err := readSecret(cfg.ControlTokenFile)
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	if cfg.Egress == "" {
		return nil, errors.New("egress: nono's proxy address is missing")
	}
	pass, err := readSecret(cfg.EgressPassFile)
	if err != nil {
		return nil, fmt.Errorf("egress credential: %w", err)
	}
	e := &Edge{
		cfg:            cfg,
		sortie:         sortie,
		controlToken:   control,
		identityHeader: cfg.IdentityHeader,
		egressAuth:     "Basic " + base64.StdEncoding.EncodeToString([]byte("nono:"+pass)),
		client:         &http.Client{Timeout: 10 * time.Second},
		log:            log,
		toFlow:         map[string]http.Handler{},
		repos:          map[string]string{},
	}
	if e.identityHeader == "" {
		e.identityHeader = DefaultIdentityHeader
	}
	e.toSortie = e.proxy("sortie", sortie, "")
	for _, f := range pathFlows {
		u, err := serviceURL("flows."+f, cfg.Flows[f])
		if err != nil {
			return nil, err
		}
		e.toFlow[f] = e.proxy(f, u, "/"+f)
	}
	for f := range cfg.Flows {
		if _, ok := e.toFlow[f]; !ok {
			return nil, fmt.Errorf("flows.%s: no such flow; egress is nono's proxy, set in egress", f)
		}
	}
	return e, nil
}

func serviceURL(field, raw string) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%s: the service URL is missing", field)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("%s: %q is not an http(s) URL without user information", field, raw)
	}
	return u, nil
}

func readSecret(file string) (string, error) {
	b, err := os.ReadFile(file) // #nosec G304 -- the configuration names the file
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("%s is empty", file)
	}
	return s, nil
}

// proxy forwards to target, with strip removed from the front of the path.
func (e *Edge) proxy(name string, target *url.URL, strip string) http.Handler {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, strip)
			pr.Out.URL.RawPath = ""
			pr.SetURL(target)
		},
		// Streams (stdio, usage) reach the worker as they are written.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			e.log.Warn("service unreachable", slog.String("service", name), slog.String("error", err.Error()))
			http.Error(w, name+" is unreachable", http.StatusBadGateway)
		},
	}
}

// Handler serves the port of one worker.
func (e *Edge) Handler(worker string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The port names the worker; whatever the request says does not.
		r.Header.Del(e.identityHeader)
		r.Header.Set(e.identityHeader, worker)
		if r.Method == http.MethodConnect {
			e.serveEgress(w, r, worker)
			return
		}
		if !strings.HasPrefix(r.RequestURI, "/") {
			http.Error(w, "the egress flow takes CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		if !isClean(r.URL.Path) {
			http.Error(w, "the path is not clean", http.StatusBadRequest)
			return
		}
		if strings.HasPrefix(r.URL.Path, workerAPIPrefix) {
			e.toSortie.ServeHTTP(w, r)
			return
		}
		flow, _, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		next, ok := e.toFlow[flow]
		if !ok {
			http.NotFound(w, r)
			return
		}
		g, ok, err := e.admit(r.Context(), bearer(r.Header.Get("Authorization")), flow)
		if err != nil {
			unavailable(w)
			return
		}
		if !ok {
			e.log.Info("flow refused", slog.String("worker", worker), slog.String("flow", flow), slog.String("path", r.URL.Path))
			w.Header().Set("WWW-Authenticate", `Bearer realm="sortie-job"`)
			http.Error(w, "the job token does not admit this flow", http.StatusUnauthorized)
			return
		}
		e.log.Debug("flow admitted", slog.String("worker", worker), slog.String("flow", flow), slog.String("job", g.Job), slog.Uint64("attempt", g.Attempt))
		next.ServeHTTP(w, r)
	})
}

func isClean(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	c := path.Clean(p)
	if strings.HasSuffix(p, "/") && c != "/" {
		c += "/"
	}
	return c == p
}

// admit asks Sortie what token grants, and admits it only for flow. An
// error means Sortie could not answer, as while it restarts: nothing is
// admitted, but the token is not refused either.
func (e *Edge) admit(ctx context.Context, token, flow string) (Grant, bool, error) {
	if token == "" {
		return Grant{}, false, nil
	}
	g, err := e.introspect(ctx, token)
	if err != nil {
		e.log.Warn("introspection failed", slog.String("flow", flow), slog.String("error", err.Error()))
		return Grant{}, false, err
	}
	if g == nil || g.Flow != flow {
		return Grant{}, false, nil
	}
	return *g, true, nil
}

// unavailable answers a request whose token Sortie could not check. It is
// 503, which a worker retries while keeping its machine; a 401 would tell
// it the control plane no longer knows the job.
func unavailable(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "1")
	http.Error(w, "Sortie cannot check the job token now; try again", http.StatusServiceUnavailable)
}

// introspect returns the grant of a live token, nil for any other.
func (e *Edge) introspect(ctx context.Context, token string) (*Grant, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	u := e.sortie.JoinPath(introspectPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+e.controlToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var g Grant
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&g); err != nil {
			return nil, fmt.Errorf("introspection answer: %w", err)
		}
		return &g, nil
	case http.StatusNotFound:
		return nil, nil
	default:
		return nil, fmt.Errorf("introspection answered %s", resp.Status)
	}
}

func bearer(h string) string {
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}

// proxyToken reads the job token of a CONNECT request: a Bearer token, or
// the password of Basic credentials (HTTPS_PROXY=http://job:TOKEN@...).
func proxyToken(r *http.Request) string {
	h := r.Header.Get("Proxy-Authorization")
	if t := bearer(h); t != "" {
		return t
	}
	const p = "Basic "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(h[len(p):]))
		if err != nil {
			return ""
		}
		_, pass, _ := strings.Cut(string(raw), ":")
		return pass
	}
	return ""
}

// serveEgress tunnels an admitted CONNECT through nono's proxy. nono
// decides the host and replaces the placeholders; a refusal is recorded
// here, with the job and attempt the token belongs to.
func (e *Edge) serveEgress(w http.ResponseWriter, r *http.Request, worker string) {
	host := r.Host
	g, ok, err := e.admit(r.Context(), proxyToken(r), egressFlow)
	if err != nil {
		unavailable(w)
		return
	}
	if !ok {
		e.log.Info("flow refused", slog.String("worker", worker), slog.String("flow", egressFlow), slog.String("host", host))
		w.Header().Set("Proxy-Authenticate", `Bearer realm="sortie-job"`)
		http.Error(w, "the job token does not admit the egress flow", http.StatusUnauthorized)
		return
	}
	attrs := []any{slog.String("worker", worker), slog.String("job", g.Job), slog.Uint64("attempt", g.Attempt), slog.String("host", host)}
	egress := e.cfg.Egress
	if e.liveProof(g.Job) {
		egress = e.cfg.LiveProof.Egress
		attrs = append(attrs, slog.String("profile", "live-proof"))
	} else if len(e.cfg.Repositories) > 0 {
		repo, err := e.jobRepository(r.Context(), g)
		if err != nil {
			e.log.Warn("job repository unknown", append(attrs, slog.String("error", err.Error()))...)
			unavailable(w)
			return
		}
		if addr, ok := e.cfg.Repositories[repo]; ok {
			egress = addr
			attrs = append(attrs, slog.String("profile", repo))
		}
	}
	upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.Context(), "tcp", egress)
	if err != nil {
		e.log.Warn("egress unreachable", append(attrs, slog.String("error", err.Error()))...)
		http.Error(w, "egress is unreachable", http.StatusBadGateway)
		return
	}
	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	_, err = fmt.Fprintf(upstream, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", host, host, e.egressAuth)
	var resp *http.Response
	br := bufio.NewReader(upstream)
	if err == nil {
		resp, err = http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	}
	if err != nil {
		_ = upstream.Close()
		e.log.Warn("egress unreachable", append(attrs, slog.String("error", err.Error()))...)
		http.Error(w, "egress is unreachable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = upstream.Close()
		e.log.Warn("egress refused", append(attrs, slog.Int("status", resp.StatusCode))...)
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	_ = resp.Body.Close()
	_ = upstream.SetDeadline(time.Time{})
	client, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = upstream.Close()
		http.Error(w, "the connection cannot be tunnelled", http.StatusInternalServerError)
		return
	}
	e.log.Info("egress tunnel", attrs...)
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	pipe(client, buf.Reader, upstream, br)
}

// liveProof reports whether job is a live proof that runs: the live-proof
// jobs file names it. The file is read on each CONNECT, so a live proof
// gets its egress from its first request and loses it once it ends.
func (e *Edge) liveProof(job string) bool {
	if e.cfg.LiveProof == nil || job == "" {
		return false
	}
	b, err := os.ReadFile(e.cfg.LiveProof.JobsFile) // #nosec G304 -- the configuration names the file
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			e.log.Warn("live-proof jobs unreadable", slog.String("error", err.Error()))
		}
		return false
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if strings.TrimSpace(line) == job {
			return true
		}
	}
	return false
}

// errJobNotRunning is a job Sortie's state does not list as running yet.
var errJobNotRunning = errors.New("no running job of Sortie's has that identifier")

// jobRepository returns the repository the attempt of g runs in, from the
// running entry of Sortie's state (GET /api/v1/state, with the control
// token): its issue_identifier is the job, and its repository the name
// the workflow gives the repository. The answer is kept for the attempt,
// so Sortie is asked once per attempt.
func (e *Edge) jobRepository(ctx context.Context, g Grant) (string, error) {
	key := g.Job + "#" + strconv.FormatUint(g.Attempt, 10)
	e.reposMu.Lock()
	repo, ok := e.repos[key]
	e.reposMu.Unlock()
	if ok {
		return repo, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.sortie.JoinPath("/api/v1/state").String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+e.controlToken)
	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the state answered %s", resp.Status)
	}
	var state struct {
		Running []struct {
			IssueIdentifier string `json:"issue_identifier"`
			Repository      string `json:"repository"`
		} `json:"running"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&state); err != nil {
		return "", fmt.Errorf("the state: %w", err)
	}
	for _, r := range state.Running {
		if r.IssueIdentifier != g.Job {
			continue
		}
		if r.Repository != "" {
			e.reposMu.Lock()
			if len(e.repos) >= 4096 {
				clear(e.repos)
			}
			e.repos[key] = r.Repository
			e.reposMu.Unlock()
		}
		return r.Repository, nil
	}
	return "", errJobNotRunning
}

// pipe copies both ways until either side ends, then closes both.
func pipe(client net.Conn, fromClient io.Reader, upstream net.Conn, fromUpstream io.Reader) {
	var once sync.Once
	done := func() { once.Do(func() { _ = client.Close(); _ = upstream.Close() }) }
	go func() { _, _ = io.Copy(upstream, fromClient); done() }()
	_, _ = io.Copy(client, fromUpstream)
	done()
}

// Bound is a worker's port, listening.
type Bound struct {
	Worker   string
	Listener net.Listener
}

// Listen opens each worker's port, on loopback only.
func (e *Edge) Listen(ctx context.Context) ([]Bound, error) {
	var lc net.ListenConfig
	var out []Bound
	for _, w := range e.cfg.Workers {
		l, err := lc.Listen(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(w.Port)))
		if err != nil {
			for _, b := range out {
				_ = b.Listener.Close()
			}
			return nil, fmt.Errorf("worker %s: %w", w.Name, err)
		}
		out = append(out, Bound{Worker: w.Name, Listener: l})
	}
	return out, nil
}

// Serve serves every bound port until ctx ends.
func (e *Edge) Serve(ctx context.Context, bound []Bound) error {
	errs := make(chan error, len(bound))
	var servers []*http.Server
	for _, b := range bound {
		s := &http.Server{Handler: e.Handler(b.Worker), ReadHeaderTimeout: 10 * time.Second}
		servers = append(servers, s)
		go func() { errs <- s.Serve(b.Listener) }()
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.Shutdown(shut)
	}
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}
