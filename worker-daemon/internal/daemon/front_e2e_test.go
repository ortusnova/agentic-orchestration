package daemon_test

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"maps"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// front stands in for the control plane's Pomerium (control-plane/,
// internal/pomerium): it takes only connections with a client certificate
// from the control plane's CA, sets the worker identity header from the
// certificate's DNS name over whatever the worker sent, and routes each
// flow as Pomerium's rendered routes do: the worker API and the stdio,
// bundles, usage and mcp flows to Sortie, and every CONNECT to the egress
// gate. The real Pomerium runs in control-plane/livetest.
type front struct {
	*httptest.Server
	sortie *url.URL
	gate   string
	mu     sync.Mutex
	flows  map[string]int
	ids    []string
}

// The control plane's CA and the files a worker host is given.
type frontPKI struct {
	pool                      *x509.CertPool
	caFile, certFile, keyFile string
	server                    tls.Certificate
}

var mcpPath = regexp.MustCompile(`^/mcp/jobs/([^/]+)/attempts/([0-9]+)(?:/.*)?$`)

func newFrontPKI(t *testing.T, worker string) *frontPKI {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e control plane CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	issue := func(serial int64, tmpl *x509.Certificate) ([]byte, *ecdsa.PrivateKey) {
		k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl.SerialNumber = big.NewInt(serial)
		tmpl.NotBefore, tmpl.NotAfter = time.Now().Add(-time.Hour), time.Now().Add(time.Hour)
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &k.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return der, k
	}
	srvDER, srvKey := issue(2, &x509.Certificate{Subject: pkix.Name{CommonName: "127.0.0.1"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	wDER, wKey := issue(3, &x509.Certificate{Subject: pkix.Name{CommonName: worker}, DNSNames: []string{worker},
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	p := &frontPKI{pool: x509.NewCertPool(), server: tls.Certificate{Certificate: [][]byte{srvDER}, PrivateKey: srvKey}}
	p.pool.AddCert(ca)
	dir := t.TempDir()
	p.caFile, p.certFile, p.keyFile = filepath.Join(dir, "ca.crt"), filepath.Join(dir, "worker.crt"), filepath.Join(dir, "worker.key")
	keyDER, _ := x509.MarshalPKCS8PrivateKey(wKey)
	for f, b := range map[string][]byte{
		p.caFile:   pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		p.certFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: wDER}),
		p.keyFile:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	} {
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func startFront(t *testing.T, p *frontPKI, sortieAPI, gate string) *front {
	t.Helper()
	u, _ := url.Parse(sortieAPI)
	f := &front{sortie: u, gate: gate, flows: map[string]int{}}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(f.serve))
	f.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{p.server},
		ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: p.pool}
	f.StartTLS()
	t.Cleanup(f.Close)
	return f
}

func (f *front) count(flow, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.flows[flow]++
	f.ids = append(f.ids, id)
}

func (f *front) admitted() map[string]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.flows)
}

// identities are the worker identities the front asserted, in order.
func (f *front) identities() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ids...)
}

func (f *front) serve(w http.ResponseWriter, r *http.Request) {
	id := r.TLS.PeerCertificates[0].DNSNames[0]
	r.Header.Set("X-Sortie-Worker-Identity", id)
	if r.Method == http.MethodConnect {
		f.count("egress", id)
		f.connect(w, r)
		return
	}
	var flow, path string
	p := r.URL.Path
	switch {
	case strings.HasPrefix(p, "/api/v1/workers/"):
		flow, path = "control", p
	case strings.HasPrefix(p, "/stdio/"), strings.HasPrefix(p, "/bundles/"), strings.HasPrefix(p, "/usage/"):
		flow, _, _ = strings.Cut(strings.TrimPrefix(p, "/"), "/")
		path = "/api/v1/flows/" + strings.TrimPrefix(p, "/")
	case mcpPath.MatchString(p):
		flow, path = "mcp", mcpPath.ReplaceAllString(p, "/api/v1/flows/mcp/jobs/${1}/attempts/${2}")
	default:
		http.NotFound(w, r)
		return
	}
	f.count(flow, id)
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.SetURL(f.sortie)
		pr.Out.URL.Path, pr.Out.URL.RawPath = path, ""
		pr.Out.Header.Set("X-Sortie-Worker-Identity", id)
	}, FlushInterval: -1}
	rp.ServeHTTP(w, r)
}

// connect forwards the CONNECT to the egress gate as Envoy does, and
// splices the two once the gate answers.
func (f *front) connect(w http.ResponseWriter, r *http.Request) {
	up, err := net.Dial("tcp", f.gate)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	r.Header.Del("Proxy-Connection")
	_, _ = fmt.Fprintf(up, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", r.Host, r.Host)
	_ = r.Header.Write(up)
	_, _ = io.WriteString(up, "\r\n")
	client, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		_ = up.Close()
		return
	}
	go func() { _, _ = io.Copy(up, buf); _ = up.Close() }()
	_, _ = io.Copy(client, bufio.NewReader(up))
	_ = client.Close()
}
