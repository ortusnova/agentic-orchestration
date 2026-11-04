package livetest

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The project as run.sh starts it.
const (
	publicHost     = "cp.test"
	identityHeader = "X-Sortie-Worker-Identity"
	protocolHeader = "X-Sortie-Worker-Protocol"
	sortieURL      = "http://10.89.71.10:7680"
)

type live struct {
	home string
	port string
}

// project skips the test unless run.sh started the project.
func project(t *testing.T) live {
	t.Helper()
	if os.Getenv("CP_LIVE") != "1" {
		t.Skip("CP_LIVE is not 1: control-plane/livetest/run.sh starts the project and runs these")
	}
	l := live{home: os.Getenv("CP_HOME"), port: os.Getenv("CP_PUBLIC_PORT")}
	if l.home == "" || l.port == "" {
		t.Fatal("CP_HOME and CP_PUBLIC_PORT must name the running project")
	}
	return l
}

// publicURL is Pomerium as worker hosts reach it.
func (l live) publicURL() string { return "https://" + publicHost + ":" + l.port }

func (l live) addr() string { return net.JoinHostPort("127.0.0.1", l.port) }

func (l live) caPool(t *testing.T) *x509.CertPool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(l.home, "ca", "ca.crt"))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(b) {
		t.Fatal("no CA certificate")
	}
	return pool
}

// tlsFor is the TLS a worker host dials Pomerium with: the certificate
// cpctl issued it, or none for "".
func (l live) tlsFor(t *testing.T, worker string) *tls.Config {
	t.Helper()
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: l.caPool(t), ServerName: publicHost}
	if worker != "" {
		dir := filepath.Join(l.home, "workers", worker)
		cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "worker.crt"), filepath.Join(dir, "worker.key"))
		if err != nil {
			t.Fatal(err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg
}

// dial opens a TLS connection to Pomerium as worker.
func (l live) dial(ctx context.Context, cfg *tls.Config) (*tls.Conn, error) {
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 10 * time.Second}, Config: cfg}
	c, err := d.DialContext(ctx, "tcp", l.addr())
	if err != nil {
		return nil, err
	}
	return c.(*tls.Conn), nil
}

// client is a worker host's HTTP client: every request goes to Pomerium.
func (l live) client(t *testing.T, worker string) *http.Client {
	t.Helper()
	cfg := l.tlsFor(t, worker)
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return l.dial(ctx, cfg) },
		Proxy:          nil,
	}}
}

var declaration = map[string]any{
	"os": "linux", "arch": "amd64", "kvm": true, "nested": true, "backends": []string{"smolvm"},
	"run_slots": 0, "running": 0, "free": map[string]float64{"cpu": 0, "mem_gib": 0, "disk_gib": 0},
}

// workerCall posts body to a worker API path through Pomerium as worker,
// with the identity header set to forged when it is not "".
func (l live) workerCall(t *testing.T, worker, path string, body any, forged string) (int, []byte, error) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(http.MethodPost, l.publicURL()+path, r)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(protocolHeader, "1")
	req.Header.Set("Content-Type", "application/json")
	if forged != "" {
		req.Header.Set(identityHeader, forged)
	}
	resp, err := l.client(t, worker).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

func (l live) checkin(t *testing.T, worker string, decl map[string]any, forged string) (int, error) {
	t.Helper()
	status, _, err := l.workerCall(t, worker, "/api/v1/workers/checkin", decl, forged)
	return status, err
}

type workerState struct {
	ID          string    `json:"id"`
	LastCheckIn time.Time `json:"last_check_in"`
}

// sortieWorkers reads the workers Sortie knows from its state, on the
// internal network: the egress gate's container is on it and holds the
// control token.
func sortieWorkers(t *testing.T) map[string]workerState {
	t.Helper()
	out := podman(t, "exec", "cp-egress-gate", "/usr/local/bin/cp-probe", "-print",
		"-bearer-file", "/run/secrets/cp-sortie-control-token", sortieURL+"/api/v1/state")
	var st struct {
		Workers *struct {
			Workers []workerState `json:"workers"`
		} `json:"workers"`
	}
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("Sortie's state: %v: %s", err, out)
	}
	m := map[string]workerState{}
	if st.Workers != nil {
		for _, w := range st.Workers.Workers {
			m[w.ID] = w
		}
	}
	return m
}

func podman(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("podman", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("podman %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

// logs returns a container's log, standard output and error.
func logs(t *testing.T, container string) string {
	t.Helper()
	out, err := exec.Command("podman", "logs", container).CombinedOutput()
	if err != nil {
		t.Fatalf("podman logs %s: %v: %s", container, err, out)
	}
	return string(out)
}

// cpsh runs the operator's cp.sh with the project's environment.
func cpsh(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("../cp.sh", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cp.sh %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func secret(t *testing.T, l live, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(l.home, "secrets", name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}

func eventually(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(time.Second)
	}
}
