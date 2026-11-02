package home

import (
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/pki"
	"github.com/ortusnova/agentic-orchestration/control-plane/internal/workers"
)

func initHome(t *testing.T) Home {
	t.Helper()
	h := Home{Dir: t.TempDir()}
	if _, err := h.Init("https://cp.example.org:8443", []string{"cp.lan"}); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestInitMakesTheCAAndTheGeneratedSecrets(t *testing.T) {
	h := initHome(t)
	ca, err := pki.LoadCA(h.CACert(), h.CAKey())
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(h.CAKey()); fi.Mode().Perm() != 0o600 {
		t.Errorf("the CA key is mode %v, want 0600", fi.Mode().Perm())
	}
	srv, err := tls.LoadX509KeyPair(h.Secret("cp-pomerium-cert"), h.Secret("cp-pomerium-key"))
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(srv.Certificate[0])
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	for _, name := range []string{"cp.example.org", "cp.lan"} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: name}); err != nil {
			t.Errorf("Pomerium's certificate for %s: %v", name, err)
		}
	}
	var missing []string
	for _, s := range h.MissingSecrets() {
		if s.Generated {
			t.Errorf("init left the generated secret %s missing", s.Name)
		}
		missing = append(missing, s.Name)
	}
	if got := strings.Join(missing, " "); got != "cp-github-token cp-anthropic-token cp-huly-token cp-workbench-password-hash" {
		t.Errorf("missing = %s, want only what the operator provides", got)
	}
	for _, s := range Secrets {
		if fi, err := os.Stat(h.Secret(s.Name)); err == nil && fi.Mode().Perm() != 0o600 {
			t.Errorf("%s is mode %v, want 0600", s.Name, fi.Mode().Perm())
		}
	}
	if _, err := os.Stat(h.PomeriumConfig()); err != nil {
		t.Errorf("init rendered no Pomerium configuration: %v", err)
	}
}

func TestInitKeepsWhatExists(t *testing.T) {
	h := initHome(t)
	before := map[string][]byte{}
	for _, f := range []string{h.CAKey(), h.Secret("cp-sortie-control-token"), h.Secret("cp-pomerium-key"), h.Secret("cp-egress-ca-key")} {
		before[f], _ = os.ReadFile(f)
	}
	made, err := h.Init("https://cp.example.org:8443", []string{"cp.lan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 0 {
		t.Errorf("a second init made %v", made)
	}
	for f, b := range before {
		if now, _ := os.ReadFile(f); string(now) != string(b) {
			t.Errorf("a second init replaced %s", f)
		}
	}
	made, err = h.Init("https://cp2.example.org:8443", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(made) != 1 || made[0] != h.Secret("cp-pomerium-cert") {
		t.Errorf("a new host made %v, want only Pomerium's certificate", made)
	}
}

func TestIssueAndRevoke(t *testing.T) {
	h := initHome(t)
	is, err := h.IssueWorker("taichi", nil)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := pki.LoadCA(h.CACert(), h.CAKey())
	cert, err := pki.LoadCert(is.Cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := ca.Verify(cert); err != nil {
		t.Fatalf("taichi's certificate: %v", err)
	}
	if len(cert.DNSNames) != 1 || cert.DNSNames[0] != "taichi" {
		t.Errorf("DNS names = %v, want [taichi]: Pomerium asserts it as the identity", cert.DNSNames)
	}
	if fi, _ := os.Stat(is.Key); fi.Mode().Perm() != 0o600 {
		t.Errorf("the worker key is mode %v", fi.Mode().Perm())
	}
	cfg, _ := os.ReadFile(h.PomeriumConfig())
	if !strings.Contains(string(cfg), pki.Fingerprint(cert)) {
		t.Fatal("the rendered configuration does not allow taichi's certificate")
	}

	// A new certificate replaces the old one on every route.
	again, err := h.IssueWorker("taichi", []string{"control"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(h.PomeriumConfig())
	if strings.Contains(string(cfg), pki.Fingerprint(cert)) || strings.Count(string(cfg), again.Worker.Fingerprint) != 1 {
		t.Error("after a new issue the configuration should allow only the new certificate, on control only")
	}

	if err := h.RevokeWorker("taichi"); err != nil {
		t.Fatal(err)
	}
	cfg, _ = os.ReadFile(h.PomeriumConfig())
	if strings.Contains(string(cfg), again.Worker.Fingerprint) {
		t.Error("the revoked certificate is still allowed")
	}
	if _, err := os.Stat(is.Dir); !os.IsNotExist(err) {
		t.Errorf("revoke left %s", is.Dir)
	}
	l, _ := workers.Load(h.WorkerList())
	if _, ok := l.Get("taichi"); ok {
		t.Error("taichi is still listed")
	}
	if err := h.RevokeWorker("taichi"); err == nil {
		t.Error("revoking an unlisted worker succeeded")
	}
}

func TestIssueRefusesABadName(t *testing.T) {
	h := initHome(t)
	for _, n := range []string{"", "Taichi", "a.b", "-x", "a_b", strings.Repeat("a", 64)} {
		if _, err := h.IssueWorker(n, nil); err == nil {
			t.Errorf("issued %q", n)
		}
	}
	if _, err := h.IssueWorker("p1", []string{"ssh"}); err == nil {
		t.Error("issued a flow that does not exist")
	}
	if _, err := os.Stat(filepath.Join(h.Dir, "workers.json")); err == nil {
		l, _ := workers.Load(h.WorkerList())
		if len(l.Workers) != 0 {
			t.Errorf("refused issues listed %v", l.Workers)
		}
	}
}
