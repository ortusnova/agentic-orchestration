package egress

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	controlToken = "control-token-of-the-test"
	placeholder  = "sortie-credential-anthropic"
	credential   = "the-credential-nono-holds"
)

// fakeSortie answers token introspection as Sortie does.
type fakeSortie struct {
	*httptest.Server
	mu     sync.Mutex
	grants map[string]Grant
}

func newFakeSortie(t *testing.T) *fakeSortie {
	f := &fakeSortie{grants: map[string]Grant{}}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != introspectPath {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+controlToken {
			http.Error(w, "the control token is required", http.StatusUnauthorized)
			return
		}
		var req struct{ Token string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		g, ok := f.grants[req.Token]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unknown or ended job token", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(g)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeSortie) mint(job, flow string) string {
	tok := "tok-" + flow + "-" + rand.Text()
	f.mu.Lock()
	f.grants[tok] = Grant{Job: job, Attempt: 1, Flow: flow}
	f.mu.Unlock()
	return tok
}

func (f *fakeSortie) end(tok string) {
	f.mu.Lock()
	delete(f.grants, tok)
	f.mu.Unlock()
}

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "egress test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &testCA{cert: cert, key: key, pool: pool}
}

func (ca *testCA) leaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// fakeNono stands in for nono's proxy: it takes CONNECT with its own
// credential, refuses a host it does not allow with 403, and for an
// allowed host intercepts TLS, replaces the placeholder with its
// credential, and answers with the Authorization it would send upstream.
type fakeNono struct {
	addr     string
	pass     string
	ca       *testCA
	mu       sync.Mutex
	connects []string
	// allowed are the hosts it tunnels besides api.anthropic.com:443.
	allowed map[string]bool
}

func startFakeNono(t *testing.T, ca *testCA) *fakeNono {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	n := &fakeNono{addr: l.Addr().String(), pass: "nono-pass-" + rand.Text(), ca: ca, allowed: map[string]bool{}}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go n.serve(c)
		}
	}()
	return n
}

func (n *fakeNono) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil {
		return
	}
	n.mu.Lock()
	n.connects = append(n.connects, req.Host)
	allowed := n.allowed[req.Host]
	n.mu.Unlock()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("nono:"+n.pass))
	switch {
	case req.Method != http.MethodConnect || req.Header.Get("Proxy-Authorization") != want:
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
		return
	case req.Host != "api.anthropic.com:443" && !allowed:
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 16\r\n\r\nhost not allowed")
		return
	}
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
	tc := tls.Server(c, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return n.ca.leaf("api.anthropic.com") }})
	inner, err := http.ReadRequest(bufio.NewReader(tc))
	if err != nil {
		return
	}
	got := strings.ReplaceAll(inner.Header.Get("Authorization"), placeholder, credential)
	_, _ = fmt.Fprintf(tc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(got), got)
}

// allow lets the fake tunnel host too.
func (n *fakeNono) allow(host string) {
	n.mu.Lock()
	n.allowed[host] = true
	n.mu.Unlock()
}

func (n *fakeNono) seen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.connects...)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t      *testing.T
	sortie *fakeSortie
	nono   *fakeNono
	ca     *testCA
	gate   *httptest.Server
	log    *syncBuffer
}

func writeSecret(t *testing.T, name, value string) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	return newHarnessWith(t, nil)
}

// newHarnessWith is newHarness with the gate's configuration changed by
// configure, when it is not nil, before the gate starts.
func newHarnessWith(t *testing.T, configure func(*harness, *Config)) *harness {
	t.Helper()
	h := &harness{t: t, sortie: newFakeSortie(t), ca: newTestCA(t), log: &syncBuffer{}}
	h.nono = startFakeNono(t, h.ca)
	cfg := Config{
		Sortie:           h.sortie.URL,
		ControlTokenFile: writeSecret(t, "control-token", controlToken),
		Nono:             h.nono.addr,
		NonoPassFile:     writeSecret(t, "nono-pass", h.nono.pass),
	}
	if configure != nil {
		configure(h, &cfg)
	}
	g, err := New(cfg, slog.New(slog.NewTextHandler(h.log, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The worker header is what Pomerium sets in front of the gate.
	h.gate = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set(DefaultIdentityHeader, "p1")
		g.ServeHTTP(w, r)
	}))
	t.Cleanup(h.gate.Close)
	return h
}

// connect sends a CONNECT for host with authorization as the proxy
// credential and returns the answer's status.
func (h *harness) connect(host, authorization string) int {
	h.t.Helper()
	u, _ := url.Parse(h.gate.URL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", host, host, authorization)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// agent is an agent in a guest: its HTTPS goes through the gate as a
// CONNECT proxy with the egress token, and it trusts nono's CA.
func (h *harness) agent(token string) *http.Client {
	u, _ := url.Parse(h.gate.URL)
	u.User = url.UserPassword("job", token)
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: h.ca.pool},
	}}
}

func TestModelRequestThroughNono(t *testing.T) {
	h := newHarness(t)
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+placeholder)
	resp, err := h.agent(h.sortie.mint("ONS-7", Flow)).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "Bearer "+credential {
		t.Fatalf("got %d %q, want 200 and the credential in place of the placeholder", resp.StatusCode, body)
	}
	want := `msg="egress tunnel" worker=p1 job=ONS-7 attempt=1 host=api.anthropic.com:443`
	if !strings.Contains(h.log.String(), want) {
		t.Errorf("the gate did not log the tunnel; want %s in:\n%s", want, h.log)
	}
}

// nono allows only the credential hosts: any other host, a harness host
// such as registry.npmjs.org too (which the worker allows directly), is
// refused on the egress flow and recorded with the worker, job and attempt.
func TestHostNotAllowed(t *testing.T) {
	for _, host := range []string{"pastebin.com", "registry.npmjs.org"} {
		t.Run(host, func(t *testing.T) {
			h := newHarness(t)
			status := h.connect(host+":443", "Bearer "+h.sortie.mint("ONS-7", Flow))
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want nono's 403", status)
			}
			want := `msg="egress refused" worker=p1 job=ONS-7 attempt=1 host=` + host + `:443 status=403`
			if !strings.Contains(h.log.String(), want) {
				t.Errorf("the refusal is not logged with its job; want %s in:\n%s", want, h.log)
			}
		})
	}
}

func TestTokensTheGateRefuses(t *testing.T) {
	h := newHarness(t)
	ended := h.sortie.mint("ONS-7", Flow)
	h.sortie.end(ended)
	basic := func(tok string) string {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte("job:"+tok))
	}
	for name, auth := range map[string]string{
		"no token":         "",
		"unknown token":    "Bearer not-a-token",
		"ended token":      "Bearer " + ended,
		"mcp token":        "Bearer " + h.sortie.mint("ONS-7", "mcp"),
		"basic, mcp token": basic(h.sortie.mint("ONS-7", "mcp")),
	} {
		if status := h.connect("api.anthropic.com:443", auth); status != http.StatusProxyAuthRequired {
			t.Errorf("%s: status = %d, want 407", name, status)
		}
	}
	if seen := h.nono.seen(); len(seen) != 0 {
		t.Errorf("refused requests reached nono: %v", seen)
	}
	if status := h.connect("api.anthropic.com:443", basic(h.sortie.mint("ONS-8", Flow))); status != http.StatusOK {
		t.Errorf("a live egress token as Basic credentials: status = %d, want 200", status)
	}
}

func TestOnlyConnect(t *testing.T) {
	h := newHarness(t)
	resp, err := http.Get(h.gate.URL + "/anything")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

// While Sortie cannot answer, as while it restarts, nothing is admitted,
// and the answer is 503, not 407: the worker dials again and keeps its
// machine, where a refusal would tell it the job is gone.
func TestSortieDownIsUnavailableNotRefused(t *testing.T) {
	h := newHarness(t)
	tok := h.sortie.mint("ONS-7", Flow)
	h.sortie.Close()
	u, _ := url.Parse(h.gate.URL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_, _ = fmt.Fprintf(c, "CONNECT api.anthropic.com:443 HTTP/1.1\r\nHost: api.anthropic.com:443\r\nProxy-Authorization: Bearer %s\r\n\r\n", tok)
	resp, err := http.ReadResponse(bufio.NewReader(c), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("with Sortie down: %d, Retry-After %q; want 503 with Retry-After", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
	if resp.Header.Get("Proxy-Authenticate") != "" {
		t.Error("with Sortie down the gate asked for another token, as for a refused one")
	}
	if seen := h.nono.seen(); len(seen) != 0 {
		t.Errorf("nono was reached with Sortie down: %v", seen)
	}
	// A request without a token is still refused: there is nothing to check.
	if status := h.connect("api.anthropic.com:443", ""); status != http.StatusProxyAuthRequired {
		t.Errorf("no token with Sortie down: %d, want 407", status)
	}
}

// agent-platform/run-supervision#A live criterion a story's machine cannot
// run becomes a live-proof job, scenario A vm chain the story's machine
// cannot run: the story's own job is refused Let's Encrypt staging by the
// kit's nono, and once stage0-live-proofs names its live proof as running,
// the same job's CONNECT goes to the live-proof nono, which allows it.
func TestALiveProofJobGetsTheLiveProofEgress(t *testing.T) {
	const staging = "acme-staging-v02.api.letsencrypt.org:443"
	jobs := filepath.Join(t.TempDir(), "active")
	var live *fakeNono
	h := newHarnessWith(t, func(h *harness, cfg *Config) {
		live = startFakeNono(t, h.ca)
		live.pass = h.nono.pass
		live.allow(staging)
		cfg.LiveProof = &LiveProof{Nono: live.addr, JobsFile: jobs}
	})

	if status := h.connect(staging, "Bearer "+h.sortie.mint("ONS-7", Flow)); status != http.StatusForbidden {
		t.Fatalf("the story's job reached Let's Encrypt staging: %d, want the kit's 403", status)
	}
	if err := os.WriteFile(jobs, []byte("ONS-12\nONS-7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := h.connect(staging, "Bearer "+h.sortie.mint("ONS-7", Flow)); status != http.StatusOK {
		t.Fatalf("the live-proof job's CONNECT answered %d, want 200 through the live-proof nono", status)
	}
	if len(h.nono.seen()) != 1 || len(live.seen()) != 1 {
		t.Errorf("connects: kit %v, live-proof %v; want 1 each", h.nono.seen(), live.seen())
	}
	if want := `msg="egress tunnel" worker=p1 job=ONS-7 attempt=1 host=` + staging + ` profile=live-proof`; !strings.Contains(h.log.String(), want) {
		t.Errorf("the live-proof tunnel is not logged with its profile; want %s in:\n%s", want, h.log)
	}

	// The live proof ends: its job is back on the kit's nono.
	if err := os.WriteFile(jobs, []byte("ONS-12\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := h.connect(staging, "Bearer "+h.sortie.mint("ONS-7", Flow)); status != http.StatusForbidden {
		t.Fatalf("after its live proof the job's CONNECT answered %d, want the kit's 403", status)
	}
}

func TestLiveProofConfig(t *testing.T) {
	control := writeSecret(t, "control-token", controlToken)
	pass := writeSecret(t, "nono-pass", "pass")
	for name, lp := range map[string]*LiveProof{
		"no nono":      {JobsFile: "/x/active"},
		"the kit's":    {Nono: "127.0.0.1:7690", JobsFile: "/x/active"},
		"no jobs file": {Nono: "127.0.0.1:7691"},
	} {
		_, err := New(Config{Sortie: "http://127.0.0.1:7680", ControlTokenFile: control, Nono: "127.0.0.1:7690", NonoPassFile: pass, LiveProof: lp}, slog.New(slog.DiscardHandler))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	lp := &LiveProof{Nono: "127.0.0.1:7691", JobsFile: "/x/active"}
	if _, err := New(Config{Sortie: "http://127.0.0.1:7680", ControlTokenFile: control, Nono: "127.0.0.1:7690", NonoPassFile: pass, LiveProof: lp}, slog.New(slog.DiscardHandler)); err != nil {
		t.Errorf("a live-proof nono: %v", err)
	}
}
