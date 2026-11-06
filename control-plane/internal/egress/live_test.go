package egress

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveNono runs the nono scenarios against the real thing: the pinned
// nono with the control plane's egress profile (../../nono) and the real
// credentials, through the gate, with a fake Sortie for introspection. It
// reaches api.anthropic.com and api.github.com, so it runs only from
// dev/stage0/test-egress-live.sh on p1, which sets CP_EGRESS_LIVE_NONO to
// the nono binary and the CP_EGRESS_* credentials as run-nono.sh does.
func TestLiveNono(t *testing.T) {
	bin := os.Getenv("CP_EGRESS_LIVE_NONO")
	if bin == "" {
		t.Skip("CP_EGRESS_LIVE_NONO is unset; dev/stage0/test-egress-live.sh runs this on p1")
	}
	for _, v := range []string{"CP_EGRESS_ANTHROPIC", "CP_EGRESS_GITHUB"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s is unset", v)
		}
	}
	var out syncBuffer
	h := newHarnessWith(t, func(h *harness, cfg *Config) {
		certFile, keyFile := h.ca.files(t)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		pass := "live-" + strconv.FormatInt(time.Now().UnixNano(), 36)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		cmd := exec.CommandContext(ctx, bin, "--silent", "proxy", "-v", "--listen", "127.0.0.1", "--port", strconv.Itoa(port),
			"--profile", "../../nono/egress-profile.json", "--proxy-ca-cert", certFile, "--proxy-ca-key", keyFile) // #nosec G204 -- the test runner names nono
		cmd.Env = append(os.Environ(), "NONO_PROXY_PASS="+pass, "NONO_NO_UPDATE_CHECK=1")
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		for i := 0; ; i++ {
			c, err := net.Dial("tcp", addr)
			if err == nil {
				_ = c.Close()
				break
			}
			if i == 100 {
				t.Fatalf("nono did not listen on %s:\n%s", addr, out.String())
			}
			time.Sleep(100 * time.Millisecond)
		}
		cfg.Nono = addr
		cfg.NonoPassFile = writeSecret(t, "nono-pass", pass)
	})

	t.Run("Model request", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+placeholder)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		resp, err := h.agent(h.sortie.mint("ONS-7", Flow)).Do(req)
		if err != nil {
			t.Fatalf("%v\nnono:\n%s", err, out.String())
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("api.anthropic.com answered %d: %s\nnono:\n%s", resp.StatusCode, body, out.String())
		}
	})

	// The github_api credential (dev/stage0/WORKFLOW.md): nono puts the
	// forge token in place of its placeholder, so GitHub counts the request
	// against the token's rate limit, not an anonymous caller's 60.
	t.Run("A credential host", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/rate_limit", nil)
		req.Header.Set("Authorization", "Bearer sortie-credential-github_api")
		req.Header.Set("Accept", "application/vnd.github+json")
		resp, err := h.agent(h.sortie.mint("ONS-7", Flow)).Do(req)
		if err != nil {
			t.Fatalf("%v\nnono:\n%s", err, out.String())
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		var rate struct {
			Resources struct {
				Core struct {
					Limit int `json:"limit"`
				} `json:"core"`
			} `json:"resources"`
		}
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &rate) != nil || rate.Resources.Core.Limit <= 60 {
			t.Fatalf("api.github.com answered %d: %.500s\nnono:\n%s", resp.StatusCode, body, out.String())
		}
	})

	// nono allows only the credential hosts. A harness host, such as
	// registry.npmjs.org or Firecrawl's search (dev/stage0/WORKFLOW.md,
	// environment.remote.egress), is the worker's to allow directly, so
	// through the egress flow it is refused like any other host, and the
	// gate records the refusal with its worker, job and attempt.
	for _, host := range []string{"registry.npmjs.org", "mcp.firecrawl.dev", "pastebin.com"} {
		t.Run("Another host "+host, func(t *testing.T) {
			status := h.connect(host+":443", "Bearer "+h.sortie.mint("ONS-7", Flow))
			if status == http.StatusOK {
				t.Fatalf("nono let the job reach %s through the egress flow", host)
			}
			want := `msg="egress refused" worker=p1 job=ONS-7 attempt=1 host=` + host + `:443 status=` + strconv.Itoa(status)
			if !strings.Contains(h.log.String(), want) {
				t.Fatalf("the refusal is not logged with its job and attempt; want %s in:\n%s", want, h.log)
			}
		})
	}
}

// files writes the CA's certificate and key, as nono's --proxy-ca-cert
// and --proxy-ca-key read them.
func (ca *testCA) files(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	der, err := x509.MarshalPKCS8PrivateKey(ca.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}
