package livetest

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/home"
	"github.com/ortusnova/agentic-orchestration/control-plane/internal/pki"
)

// Scenario: Health. The operator starts the project and runs its health
// check; it lists Sortie, acp-broker, the workbench, Pomerium, nono and
// Huly, each up.
func TestHealth(t *testing.T) {
	project(t)
	out := cpsh(t, "health")
	got := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			got[f[0]] = f[1]
		}
	}
	for _, svc := range []string{"sortie", "acp-broker", "workbench", "pomerium", "nono", "huly"} {
		if got[svc] != "up" {
			t.Errorf("health says %s is %q, want up:\n%s", svc, got[svc], out)
		}
	}
}

// The project runs as the unprivileged user, under rootless podman.
func TestRootless(t *testing.T) {
	project(t)
	if os.Getuid() == 0 {
		t.Fatal("the live tests run as root; the project must run as an unprivileged user")
	}
	if got := strings.TrimSpace(podman(t, "info", "--format", "{{.Host.Security.Rootless}}")); got != "true" {
		t.Fatalf("podman rootless = %s", got)
	}
}

// Scenario: No secret in the definition. The compose files and every image
// the project runs are searched for the credentials and the other secrets;
// none is found.
func TestNoSecretInTheDefinition(t *testing.T) {
	l := project(t)
	needles := map[string][]byte{}
	for _, s := range home.Secrets {
		v := secret(t, l, s.Name)
		if strings.Contains(s.Name, "-cert") {
			continue // a certificate is public
		}
		if s.Name == "cp-pomerium-key" || s.Name == "cp-egress-ca-key" {
			// A PEM key: search for its body, not the shared header.
			v = strings.Join(strings.Split(v, "\n")[1:2], "")
		}
		if len(v) < 16 {
			t.Fatalf("secret %s is too short to search for", s.Name)
		}
		needles[s.Name] = []byte(v)
	}
	files, _ := filepath.Glob("../compose.yaml")
	more, _ := filepath.Glob("compose.test.yaml")
	files = append(files, more...)
	nono, _ := filepath.Glob("../nono/*")
	files = append(files, nono...)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for name, n := range needles {
			if bytes.Contains(b, n) {
				t.Errorf("%s holds the secret %s", f, name)
			}
		}
	}

	var images []string
	for line := range strings.SplitSeq(strings.TrimSpace(podman(t, "ps", "-a", "--filter", "name=^cp-", "--format", "{{.ImageID}}")), "\n") {
		if line != "" && !slices.Contains(images, line) {
			images = append(images, line)
		}
	}
	if len(images) < 6 {
		t.Fatalf("found %d images of the project, want every service's", len(images))
	}
	for _, img := range images {
		found := searchImage(t, img, needles)
		for _, name := range found {
			t.Errorf("image %s holds the secret %s", img, name)
		}
	}
}

// searchImage streams `podman image save` of img, its configuration and
// every file of every layer, and returns the needles it holds.
func searchImage(t *testing.T, img string, needles map[string][]byte) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "image", "save", "--format", "docker-archive", img)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	var scanned int64
	outer := tar.NewReader(bufio.NewReaderSize(out, 1<<20))
	for {
		hdr, err := outer.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("image %s: %v", img, err)
		}
		if strings.HasSuffix(hdr.Name, ".tar") || strings.HasSuffix(hdr.Name, "/layer.tar") {
			inner := tar.NewReader(outer)
			for {
				_, err := inner.Next()
				if err != nil {
					break
				}
				scanned += scan(inner, needles, found)
			}
			continue
		}
		scanned += scan(outer, needles, found)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("podman image save %s: %v", img, err)
	}
	if scanned == 0 {
		t.Fatalf("image %s: nothing scanned", img)
	}
	var names []string
	for n := range found {
		names = append(names, n)
	}
	return names
}

// scan reads r through, recording each needle it holds, and returns the
// bytes read. A needle split across two chunks is found too.
func scan(r io.Reader, needles map[string][]byte, found map[string]bool) int64 {
	longest := 0
	for _, n := range needles {
		longest = max(longest, len(n))
	}
	buf := make([]byte, 1<<20)
	carry := 0
	var total int64
	for {
		n, err := r.Read(buf[carry:])
		total += int64(n)
		window := buf[:carry+n]
		for name, needle := range needles {
			if bytes.Contains(window, needle) {
				found[name] = true
			}
		}
		if err != nil {
			return total
		}
		keep := min(len(window), longest)
		copy(buf, window[len(window)-keep:])
		carry = keep
	}
}

// Scenario: No certificate. A client that connects to Pomerium without a
// certificate, or with one another CA issued, is rejected: no request of
// it reaches a route.
func TestNoCertificate(t *testing.T) {
	l := project(t)
	other, err := pki.NewCA("not the control plane's CA")
	if err != nil {
		t.Fatal(err)
	}
	forged, _ := other.IssueWorker("p1")
	keyPEM, _ := pki.KeyPEM(forged.Key)
	forgedCert, err := tls.X509KeyPair(pki.CertPEM(forged.Cert), keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	for name, certs := range map[string][]tls.Certificate{"no certificate": nil, "another CA's certificate": {forgedCert}} {
		t.Run(name, func(t *testing.T) {
			cfg := l.tlsFor(t, "")
			cfg.Certificates = certs
			c := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
				DialTLSContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return l.dial(ctx, cfg) },
			}}
			for _, path := range []string{"/api/v1/workers/checkin", "/stdio/jobs/ONS-7/attempts/1", "/usage/jobs/ONS-7/attempts/1"} {
				req, _ := http.NewRequest(http.MethodPost, l.publicURL()+path, strings.NewReader("{}"))
				req.Header.Set(protocolHeader, "1")
				resp, err := c.Do(req)
				if err == nil {
					_ = resp.Body.Close()
					t.Errorf("POST %s with %s: Pomerium answered %s; want the connection rejected", path, name, resp.Status)
				}
			}
		})
	}
}

// Scenario: A forged identity header. Worker p1 sends
// X-Sortie-Worker-Identity: taichi; Sortie sees p1 as the caller.
func TestForgedIdentityHeader(t *testing.T) {
	l := project(t)
	before := sortieWorkers(t)
	start := time.Now().Add(-time.Second)
	status, err := l.checkin(t, "p1", declaration, "taichi")
	if err != nil || status != http.StatusOK {
		t.Fatalf("p1's check-in: %d %v", status, err)
	}
	after := sortieWorkers(t)
	p1, ok := after["p1"]
	if !ok || p1.LastCheckIn.Before(start) {
		t.Fatalf("Sortie did not record p1's check-in: %+v", after)
	}
	if tai, ok := after["taichi"]; ok && !tai.LastCheckIn.Equal(before["taichi"].LastCheckIn) {
		t.Fatalf("Sortie took p1's check-in as taichi's: %+v", after)
	}
}

// Scenario: Revoked worker. The operator removes taichi's certificate
// from the worker list and applies it; taichi's next check-in is
// rejected, and p1 still checks in.
func TestRevokedWorker(t *testing.T) {
	l := project(t)
	if status, err := l.checkin(t, "taichi", declaration, ""); err != nil || status != http.StatusOK {
		t.Fatalf("taichi's check-in before the revocation: %d %v", status, err)
	}
	cert := filepath.Join(l.home, "workers", "taichi", "worker.crt")
	key := filepath.Join(l.home, "workers", "taichi", "worker.key")
	saved := t.TempDir()
	for _, f := range []string{cert, key} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(saved, filepath.Base(f)), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		// Put taichi back for the tests that follow: a new certificate.
		cpsh(t, "worker", "issue", "taichi")
		cpsh(t, "apply")
	})

	cpsh(t, "worker", "revoke", "taichi")

	// The worker keeps its certificate; the control plane no longer lists it.
	if err := os.MkdirAll(filepath.Dir(cert), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{cert, key} {
		b, _ := os.ReadFile(filepath.Join(saved, filepath.Base(f)))
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	status, err := l.checkin(t, "taichi", declaration, "")
	if err == nil && status < 300 {
		t.Fatalf("taichi's check-in after the revocation was answered %d", status)
	}
	t.Logf("taichi's check-in after the revocation: status %d, error %v", status, err)
	for _, path := range []string{"/usage/jobs/ONS-7/attempts/1", "/bundles/jobs/ONS-7/attempts/1/seed"} {
		status, _, err := l.workerCall(t, "taichi", path, map[string]any{}, "")
		if err == nil && status != http.StatusForbidden {
			t.Errorf("taichi reached %s after the revocation: %d, want Pomerium's 403", path, status)
		}
	}
	if status, err := l.checkin(t, "p1", declaration, ""); err != nil || status != http.StatusOK {
		t.Fatalf("p1's check-in after taichi's revocation: %d %v", status, err)
	}
}
