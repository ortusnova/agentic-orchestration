package cp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/config"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

type pki struct {
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte
	serial int64
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return &pki{ca: ca, caKey: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), serial: 1}
}

func (p *pki) issue(t *testing.T, cn string, usage x509.ExtKeyUsage) (certPEM, keyPEM []byte) {
	t.Helper()
	p.serial++
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(p.serial), Subject: pkix.Name{CommonName: cn},
		ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.ca, &key.PublicKey, p.caKey)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder})
}

// Once the control plane issues client certificates (design D18), the
// daemon presents its own on every request and trusts the configured
// CA; without the certificate the control plane refuses it.
func TestClientCertificate(t *testing.T) {
	p := newPKI(t)
	srvCert, srvKey := p.issue(t, "control plane", x509.ExtKeyUsageServerAuth)
	cliCert, cliKey := p.issue(t, "taichi", x509.ExtKeyUsageClientAuth)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	caFile, certFile, keyFile := write("ca.pem", p.caPEM), write("cert.pem", cliCert), write("key.pem", cliKey)

	seen := make(chan string, 4)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.TLS.PeerCertificates[0].Subject.CommonName + " " + r.Header.Get(protocol.ProtocolHeader) + " " + r.Header.Get("X-Sortie-Worker-Identity")
		_, _ = w.Write([]byte(`{"heartbeat_ms":1000,"lease_ms":3000}`))
	}))
	pair, err := tls.X509KeyPair(srvCert, srvKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(p.ca)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}
	srv.StartTLS()
	defer srv.Close()

	cfg, err := TLSConfig(config.TLS{CertFile: certFile, KeyFile: keyFile, CAFile: caFile})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{BaseURL: srv.URL, WorkerID: "taichi", IdentityHeader: "X-Sortie-Worker-Identity", HTTP: HTTPClient(cfg)}
	tm, err := c.CheckIn(context.Background(), protocol.Declaration{})
	if err != nil {
		t.Fatal(err)
	}
	if tm.HeartbeatMS != 1000 || tm.LeaseMS != 3000 {
		t.Errorf("timings = %+v", tm)
	}
	if got := <-seen; got != "taichi 1 taichi" {
		t.Errorf("server saw %q, want the client certificate, protocol 1 and the identity", got)
	}

	noCert, _ := TLSConfig(config.TLS{CAFile: caFile})
	c.HTTP = HTTPClient(noCert)
	if _, err := c.CheckIn(context.Background(), protocol.Declaration{}); err == nil {
		t.Error("check-in without the client certificate should fail")
	}
}

func TestAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case protocol.PollPath:
			w.WriteHeader(http.StatusConflict)
		case protocol.AttemptPath("ONS-7", 1, "heartbeat"), protocol.AttemptPath("ONS-7", 1, "report"):
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_checkin","message":"bad"}}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, WorkerID: "w", IdentityHeader: "X-Sortie-Worker-Identity", HTTP: HTTPClient(nil)}
	ctx := context.Background()
	if _, err := c.Poll(ctx); !errors.Is(err, ErrNotCheckedIn) {
		t.Errorf("poll 409 = %v", err)
	}
	if _, err := c.Heartbeat(ctx, "ONS-7", 1, protocol.Beat{}); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("heartbeat 409 = %v", err)
	}
	if err := c.Report(ctx, "ONS-7", 1, protocol.Report{}); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("report 409 = %v", err)
	}
	var se *StatusError
	if _, err := c.CheckIn(ctx, protocol.Declaration{}); !errors.As(err, &se) || se.Code != "invalid_checkin" {
		t.Errorf("check-in 400 = %v", err)
	}
}

// A heartbeat or report answered 410 is ErrJobGone, carrying the
// control plane's reason; 503 is any other refusal, which the daemon
// retries.
func TestJobGoneAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case protocol.AttemptPath("ONS-19", 1, "heartbeat"), protocol.AttemptPath("ONS-19", 1, "report"):
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":{"code":"job_gone","message":"Sortie withdrew attempt 1 of job ONS-19"}}`))
		default:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"recovering","message":"adopting"}}`))
		}
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, WorkerID: "w", IdentityHeader: "X-Sortie-Worker-Identity", HTTP: HTTPClient(nil)}
	ctx := context.Background()
	_, err := c.Heartbeat(ctx, "ONS-19", 1, protocol.Beat{})
	var se *StatusError
	if !errors.Is(err, ErrJobGone) || !errors.As(err, &se) || se.Message != "Sortie withdrew attempt 1 of job ONS-19" {
		t.Errorf("heartbeat 410 = %v, want ErrJobGone with the reason", err)
	}
	if err := c.Report(ctx, "ONS-19", 1, protocol.Report{}); !errors.Is(err, ErrJobGone) {
		t.Errorf("report 410 = %v, want ErrJobGone", err)
	}
	if _, err := c.Heartbeat(ctx, "ONS-7", 1, protocol.Beat{}); errors.Is(err, ErrJobGone) || errors.Is(err, ErrLeaseLost) || !errors.As(err, &se) || se.Status != http.StatusServiceUnavailable {
		t.Errorf("heartbeat 503 = %v, want a plain refusal", err)
	}
}
