// Package egress is the egress gate: the service behind Pomerium's
// egress route. Pomerium has admitted the worker by its client
// certificate and named it in the identity header; the gate admits each
// CONNECT only when Sortie's token introspection says its job token is
// live and granted for the egress flow, logs it with the worker, job and
// attempt, and tunnels it through nono's proxy, which holds the
// credentials and decides the host.
package egress

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
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	// Flow is the job-token flow the gate admits.
	Flow = "egress"

	// DefaultIdentityHeader is Sortie's default worker.identity_header.
	DefaultIdentityHeader = "X-Sortie-Worker-Identity"

	introspectPath = "/api/v1/tokens/introspect"
)

// Config configures the gate.
type Config struct {
	// Sortie is the base URL of Sortie on the internal network.
	Sortie string
	// ControlTokenFile holds Sortie's control token, which introspection needs.
	ControlTokenFile string
	// IdentityHeader names the worker; Pomerium sets it.
	IdentityHeader string
	// Nono is the host:port of nono's proxy.
	Nono string
	// NonoPassFile holds the credential nono's proxy takes.
	NonoPassFile string
}

// Grant is what Sortie's introspection says a live token grants.
type Grant struct {
	Job     string `json:"job"`
	Attempt uint64 `json:"attempt"`
	Flow    string `json:"flow"`
}

// Gate serves the egress route.
type Gate struct {
	cfg            Config
	sortie         *url.URL
	controlToken   string
	nonoAuth       string
	identityHeader string
	client         *http.Client
	log            *slog.Logger
}

// New reads the secret files and builds the gate.
func New(cfg Config, log *slog.Logger) (*Gate, error) {
	u, err := url.Parse(cfg.Sortie)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("sortie: %q is not an http(s) URL without user information", cfg.Sortie)
	}
	if cfg.Nono == "" {
		return nil, errors.New("nono: the proxy address is missing")
	}
	control, err := ReadSecret(cfg.ControlTokenFile)
	if err != nil {
		return nil, fmt.Errorf("control token: %w", err)
	}
	pass, err := ReadSecret(cfg.NonoPassFile)
	if err != nil {
		return nil, fmt.Errorf("nono credential: %w", err)
	}
	g := &Gate{
		cfg:            cfg,
		sortie:         u,
		controlToken:   control,
		nonoAuth:       "Basic " + base64.StdEncoding.EncodeToString([]byte("nono:"+pass)),
		identityHeader: cfg.IdentityHeader,
		client:         &http.Client{Timeout: 10 * time.Second},
		log:            log,
	}
	if g.identityHeader == "" {
		g.identityHeader = DefaultIdentityHeader
	}
	return g, nil
}

// ReadSecret reads a secret file and trims it; an empty one is an error.
func ReadSecret(file string) (string, error) {
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

// ServeHTTP serves one request of the egress route.
func (g *Gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	worker := r.Header.Get(g.identityHeader)
	if r.Method != http.MethodConnect {
		g.log.Info("egress refused", slog.String("worker", worker), slog.String("method", r.Method), slog.String("reason", "not CONNECT"))
		http.Error(w, "the egress flow takes CONNECT only", http.StatusMethodNotAllowed)
		return
	}
	host := r.Host
	grant, ok := g.admit(r.Context(), proxyToken(r))
	if !ok {
		g.log.Info("egress refused", slog.String("worker", worker), slog.String("host", host), slog.String("reason", "no live egress token"))
		w.Header().Set("Proxy-Authenticate", `Bearer realm="sortie-job"`)
		http.Error(w, "the job token does not admit the egress flow", http.StatusProxyAuthRequired)
		return
	}
	attrs := []any{slog.String("worker", worker), slog.String("job", grant.Job), slog.Uint64("attempt", grant.Attempt), slog.String("host", host)}
	upstream, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(r.Context(), "tcp", g.cfg.Nono)
	if err != nil {
		g.log.Warn("egress unreachable", append(attrs, slog.String("error", err.Error()))...)
		http.Error(w, "egress is unreachable", http.StatusBadGateway)
		return
	}
	_ = upstream.SetDeadline(time.Now().Add(30 * time.Second))
	_, err = fmt.Fprintf(upstream, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", host, host, g.nonoAuth)
	var resp *http.Response
	br := bufio.NewReader(upstream)
	if err == nil {
		resp, err = http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	}
	if err != nil {
		_ = upstream.Close()
		g.log.Warn("egress unreachable", append(attrs, slog.String("error", err.Error()))...)
		http.Error(w, "egress is unreachable", http.StatusBadGateway)
		return
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		_ = upstream.Close()
		g.log.Info("egress refused", append(attrs, slog.Int("status", resp.StatusCode), slog.String("reason", "nono refused"))...)
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
	g.log.Info("egress tunnel", attrs...)
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	pipe(client, buf.Reader, upstream, br)
}

// admit asks Sortie what token grants, and admits only an egress grant.
func (g *Gate) admit(ctx context.Context, token string) (Grant, bool) {
	if token == "" {
		return Grant{}, false
	}
	grant, err := g.introspect(ctx, token)
	if err != nil {
		g.log.Warn("introspection failed", slog.String("error", err.Error()))
		return Grant{}, false
	}
	if grant == nil || grant.Flow != Flow {
		return Grant{}, false
	}
	return *grant, true
}

// introspect returns the grant of a live token, nil for any other.
func (g *Gate) introspect(ctx context.Context, token string) (*Grant, error) {
	body, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.sortie.JoinPath(introspectPath).String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.controlToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
		var grant Grant
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&grant); err != nil {
			return nil, fmt.Errorf("introspection answer: %w", err)
		}
		return &grant, nil
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

// pipe copies both ways until either side ends, then closes both.
func pipe(client net.Conn, fromClient io.Reader, upstream net.Conn, fromUpstream io.Reader) {
	var once sync.Once
	done := func() { once.Do(func() { _ = client.Close(); _ = upstream.Close() }) }
	go func() { _, _ = io.Copy(upstream, fromClient); done() }()
	_, _ = io.Copy(client, fromUpstream)
	done()
}
