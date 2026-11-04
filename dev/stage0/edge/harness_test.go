package main

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
	"encoding/pem"
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

// The placeholder Sortie gives a machine for the credential named
// anthropic (internal/orchestrator/remote.go in ortusnova/sortie).
const anthropicPlaceholder = "sortie-credential-anthropic"

const controlToken = "control-token-of-p1"

// fakeSortie answers token introspection, as Sortie does, and records
// what reaches its worker API.
type fakeSortie struct {
	*httptest.Server
	mu         sync.Mutex
	grants     map[string]Grant
	identities [][]string
	paths      []string
}

func newFakeSortie(t *testing.T) *fakeSortie {
	f := &fakeSortie{grants: map[string]Grant{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/tokens/introspect", func(w http.ResponseWriter, r *http.Request) {
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
	})
	mux.HandleFunc("/api/v1/workers/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.identities = append(f.identities, r.Header.Values(DefaultIdentityHeader))
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		_, _ = io.WriteString(w, `{"heartbeat_ms": 20000, "lease_ms": 60000}`)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// mint makes a live token for job ONS-7, attempt 1, on flow.
func (f *fakeSortie) mint(flow string) string {
	tok := "tok-" + flow + "-" + rand.Text()
	f.mu.Lock()
	f.grants[tok] = Grant{Job: "ONS-7", Attempt: 1, Flow: flow}
	f.mu.Unlock()
	return tok
}

func (f *fakeSortie) end(tok string) {
	f.mu.Lock()
	delete(f.grants, tok)
	f.mu.Unlock()
}

// service records the requests that reach one flow's service.
type service struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []*http.Request
}

func newService(t *testing.T, name string) *service {
	s := &service{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Clone(r.Context()))
		s.mu.Unlock()
		_, _ = io.WriteString(w, name+" served "+r.URL.Path)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *service) hits() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.reqs)
}

// syncBuffer is the edge's log, for tests to read.
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
	t        *testing.T
	sortie   *fakeSortie
	services map[string]*service
	edge     *Edge
	log      *syncBuffer
	ports    map[string]*httptest.Server
}

func writeSecret(t *testing.T, name, value string) string {
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// newHarness starts a fake Sortie, a service per path flow and an edge
// for workers taichi and mac, with egress at nono (host:port).
func newHarness(t *testing.T, nono, nonoPass string) *harness {
	t.Helper()
	h := &harness{t: t, sortie: newFakeSortie(t), services: map[string]*service{}, log: &syncBuffer{}, ports: map[string]*httptest.Server{}}
	flows := map[string]string{}
	for _, f := range pathFlows {
		h.services[f] = newService(t, f)
		flows[f] = h.services[f].URL + "/svc"
	}
	cfg := Config{
		Sortie:           h.sortie.URL,
		ControlTokenFile: writeSecret(t, "sortie-token", controlToken),
		Workers:          []Worker{{Name: "taichi", Port: 7701}, {Name: "mac", Port: 7702}},
		Flows:            flows,
		Egress:           nono,
		EgressPassFile:   writeSecret(t, "nono-pass", nonoPass),
	}
	e, err := New(cfg, slog.New(slog.NewTextHandler(h.log, &slog.HandlerOptions{Level: slog.LevelDebug})))
	if err != nil {
		t.Fatal(err)
	}
	h.edge = e
	for _, w := range cfg.Workers {
		s := httptest.NewServer(e.Handler(w.Name))
		t.Cleanup(s.Close)
		h.ports[w.Name] = s
	}
	return h
}

// do sends req to worker's port and returns the status and body.
func (h *harness) do(worker string, req *http.Request) (int, string) {
	h.t.Helper()
	u, _ := url.Parse(h.ports[worker].URL)
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// connect sends a CONNECT for host through worker's port with token as
// the proxy credential, and returns the status of the answer.
func (h *harness) connect(worker, host, token string) int { //nolint:unparam // the harness serves several workers; today's tests dial one
	h.t.Helper()
	u, _ := url.Parse(h.ports[worker].URL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { _ = c.Close() })
	auth := base64.StdEncoding.EncodeToString([]byte("job:" + token))
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", host, host, auth)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		h.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// testCA is a CA for TLS interception: an ECDSA P-256 key, as nono's
// --proxy-ca-key takes.
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
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "stage0 egress test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
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

// files writes the CA as PEM files, the certificate and a PKCS#8 key.
func (ca *testCA) files(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	pkcs8, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func (ca *testCA) leaf(host string) (*tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// fakeNono stands in for nono's proxy where the real one cannot run: it
// takes CONNECT with its own credential, refuses a host it does not allow
// with 403, and for an allowed host intercepts TLS with ca, replaces each
// placeholder in Authorization with its credential, and hands the request
// to upstream. TestLiveNono runs the real nono.
type fakeNono struct {
	addr     string
	pass     string
	allowed  map[string]bool
	creds    map[string]string // placeholder -> credential
	ca       *testCA
	upstream http.Handler
	mu       sync.Mutex
	connects int
}

func startFakeNono(t *testing.T, ca *testCA, upstream http.Handler) *fakeNono {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	n := &fakeNono{
		addr:     l.Addr().String(),
		pass:     "nono-pass-" + rand.Text(),
		allowed:  map[string]bool{"api.anthropic.com:443": true},
		creds:    map[string]string{anthropicPlaceholder: "the-credential-on-p1"},
		ca:       ca,
		upstream: upstream,
	}
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
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	n.mu.Lock()
	n.connects++
	n.mu.Unlock()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("nono:"+n.pass))
	switch {
	case req.Method != http.MethodConnect || req.Header.Get("Proxy-Authorization") != want:
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
		return
	case !n.allowed[req.Host]:
		_, _ = io.WriteString(c, "HTTP/1.1 403 Forbidden\r\nContent-Length: 15\r\n\r\nhost not allowed")
		return
	}
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
	host, _, _ := net.SplitHostPort(req.Host)
	tc := tls.Server(c, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return n.ca.leaf(host) }})
	tr := bufio.NewReader(tc)
	for {
		inner, err := http.ReadRequest(tr)
		if err != nil {
			return
		}
		for ph, cred := range n.creds {
			if v := inner.Header.Get("Authorization"); strings.Contains(v, ph) {
				inner.Header.Set("Authorization", strings.ReplaceAll(v, ph, cred))
			}
		}
		rec := httptest.NewRecorder()
		n.upstream.ServeHTTP(rec, inner)
		if err := rec.Result().Write(tc); err != nil {
			return
		}
	}
}

func (n *fakeNono) connectCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.connects
}

// agentClient is an agent in a machine on worker: its HTTPS goes through
// the edge's port as a CONNECT proxy, with the egress token, and it
// trusts the interception CA.
func agentClient(proxyURL, token string, ca *x509.CertPool) *http.Client {
	u, _ := url.Parse(proxyURL)
	u.User = url.UserPassword("job", token)
	return &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(u),
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: ca},
		},
	}
}
