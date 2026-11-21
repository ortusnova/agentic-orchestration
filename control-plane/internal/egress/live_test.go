package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLiveNono runs the nono scenarios against the real thing: the pinned
// nono with the control plane's egress profile (../../nono) and the real
// credentials, through the gate, with a fake Sortie for introspection. It
// reaches api.anthropic.com and mcp.firecrawl.dev, so it runs only from
// dev/stage0/test-egress-live.sh on p1, which sets CP_EGRESS_LIVE_NONO to
// the nono binary and the CP_EGRESS_* credentials as run-nono.sh does.
func TestLiveNono(t *testing.T) {
	bin := os.Getenv("CP_EGRESS_LIVE_NONO")
	if bin == "" {
		t.Skip("CP_EGRESS_LIVE_NONO is unset; dev/stage0/test-egress-live.sh runs this on p1")
	}
	for _, v := range []string{"CP_EGRESS_ANTHROPIC", "CP_EGRESS_GITHUB", "CP_EGRESS_GITHUB_BASIC"} {
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

	// opencode's websearch (dev/stage0/pack/opencode.json) calls
	// Firecrawl's keyless MCP; nono tunnels it without a credential.
	t.Run("Web search", func(t *testing.T) {
		call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"firecrawl_search","arguments":{"query":"smolvm","limit":2}}}`
		req, _ := http.NewRequest(http.MethodPost, "https://mcp.firecrawl.dev/v2/mcp", strings.NewReader(call))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		agent := h.agent(h.sortie.mint("ONS-7", Flow))
		agent.Transport.(*http.Transport).TLSClientConfig.RootCAs = nil // Firecrawl's own certificate: nono does not intercept it
		resp, err := agent.Do(req)
		if err != nil {
			t.Fatalf("%v\nnono:\n%s", err, out.String())
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `\"web\":[{\"url\"`) {
			t.Fatalf("mcp.firecrawl.dev answered %d: %.500s\nnono:\n%s", resp.StatusCode, body, out.String())
		}
	})

	t.Run("A host not allowed", func(t *testing.T) {
		status := h.connect("pastebin.com:443", "Bearer "+h.sortie.mint("ONS-7", Flow))
		if status == http.StatusOK {
			t.Fatal("nono let the job reach pastebin.com")
		}
		want := `msg="egress refused" worker=p1 job=ONS-7 attempt=1 host=pastebin.com:443 status=` + strconv.Itoa(status)
		if !strings.Contains(h.log.String(), want) {
			t.Fatalf("the refusal is not logged with its job and attempt; want %s in:\n%s", want, h.log)
		}
	})
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

// TestLiveForgeNono runs an attempt's GitHub egress with the real nono
// (CP_EGRESS_LIVE_NONO names the binary; no credential is needed): two
// HTTPS echo servers stand in for api.github.com and github.com, and the
// attempts' nonos run this package's cp-egress for each capture. It
// checks what the unit tests' forgeNono assumes of nono.
func TestLiveForgeNono(t *testing.T) {
	bin := os.Getenv("CP_EGRESS_LIVE_NONO")
	if bin == "" {
		t.Skip("CP_EGRESS_LIVE_NONO is unset; dev/stage0/test-egress-live.sh runs this on p1")
	}
	exe := filepath.Join(t.TempDir(), "cp-egress")
	if out, err := exec.Command("go", "build", "-o", exe, "../../cmd/cp-egress").CombinedOutput(); err != nil {
		t.Fatalf("building cp-egress: %v\n%s", err, out)
	}
	type seen struct{ path, auth string }
	var mu sync.Mutex
	var got []seen
	echo := func(ca *testCA) *httptest.Server {
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			got = append(got, seen{r.URL.Path, r.Header.Get("Authorization")})
			mu.Unlock()
			_, _ = io.WriteString(w, r.Header.Get("Authorization"))
		}))
		leaf, err := ca.leaf("localhost")
		if err != nil {
			t.Fatal(err)
		}
		s.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{*leaf}}
		s.StartTLS()
		t.Cleanup(s.Close)
		return s
	}
	dir := filepath.Join(t.TempDir(), "forge")
	var api, git *httptest.Server
	h := newHarnessWith(t, func(h *harness, cfg *Config) {
		certFile, keyFile := h.ca.files(t)
		t.Setenv("SSL_CERT_FILE", certFile) // the attempts' nonos trust the echo servers
		api, git = echo(h.ca), echo(h.ca)
		h.nono.allow(strings.TrimPrefix(api.URL, "https://"))
		cfg.ForgeTokens = &ForgeTokens{Nono: bin, CACert: certFile, CAKey: keyFile, Dir: dir, Capture: []string{exe, CredentialCommand}}
	})
	t.Cleanup(h.g.forge.close)
	apiURL := strings.Replace(api.URL, "127.0.0.1", "localhost", 1)
	gitURL := strings.Replace(git.URL, "127.0.0.1", "localhost", 1)
	h.g.forge.upstreams = forgeUpstreams{API: apiURL, Git: gitURL}
	h.nono.allow(strings.TrimPrefix(apiURL, "https://"))

	register := func(job string, tokens map[string]string) {
		t.Helper()
		if err := h.g.forge.register(ForgeRegistration{Job: job, Attempt: 1, Tokens: tokens}); err != nil {
			t.Fatalf("registering %s: %v\n%s", job, err, h.log)
		}
	}
	register("ONS-7", map[string]string{"ortusnova": "ghs_seven"})
	register("ONS-8", map[string]string{"ortusnova": "ghs_eight", "briancappello": "ghs_brian"})
	seven, eight, nine := h.sortie.mint("ONS-7", Flow), h.sortie.mint("ONS-8", Flow), h.sortie.mint("ONS-9", Flow)
	do := func(token, rawURL string) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
		req.Header.Set("Authorization", "Bearer sortie-credential-github-ortusnova")
		resp, err := h.agent(token).Do(req)
		if err != nil {
			t.Fatalf("%s: %v\n%s", rawURL, err, h.log)
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp.StatusCode, string(b)
	}
	b64 := func(s string) string { return "Basic " + base64.StdEncoding.EncodeToString([]byte(s)) }
	var wg sync.WaitGroup
	for _, c := range []struct{ token, url, want string }{
		{seven, apiURL + "/repos/ortusnova/fleet-vm", "Bearer ghs_seven"},
		{seven, apiURL + "/graphql", "Bearer ghs_seven"},
		{seven, gitURL + "/ortusnova/fleet-vm.git/info/refs", b64("x-access-token:ghs_seven")},
		{eight, apiURL + "/repos/ortusnova/fleet-vm", "Bearer ghs_eight"},
		{eight, apiURL + "/repos/briancappello/personal-site", "Bearer ghs_brian"},
		{eight, gitURL + "/briancappello/personal-site.git/info/refs", b64("x-access-token:ghs_brian")},
	} {
		wg.Go(func() {
			if st, body := do(c.token, c.url); st != http.StatusOK || body != c.want {
				t.Errorf("%s: %d %q, want %q", c.url, st, body, c.want)
			}
		})
	}
	wg.Wait()
	if st, _ := do(eight, apiURL+"/graphql"); st != http.StatusServiceUnavailable {
		t.Errorf("two owners, /graphql: %d, want nono's 503", st)
	}
	if st, body := do(nine, apiURL+"/repos/ortusnova/fleet-vm"); st != http.StatusOK || body != "Bearer sortie-credential-github-ortusnova" {
		t.Errorf("an unregistered attempt: %d %q, want the shared nono and the placeholder", st, body)
	}

	register("ONS-7", map[string]string{"ortusnova": "ghs_seven_refreshed"})
	if _, body := do(seven, apiURL+"/repos/ortusnova/fleet-vm"); body != "Bearer ghs_seven_refreshed" {
		t.Errorf("after the refresh: %q", body)
	}

	addr := h.g.forge.reg["ONS-7#1"].addr
	h.g.forge.unregister("ONS-7", 1, "test")
	if c, err := net.Dial("tcp", addr); err == nil {
		_ = c.Close()
		t.Error("ONS-7's nono still listens")
	}
	h.g.forge.unregister("ONS-8", 1, "test")
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("the forge directory holds %d entries after both attempts ended", len(entries))
	}
	if strings.Contains(h.log.String(), "ghs_") {
		t.Errorf("the gate or a nono logged a token:\n%s", h.log)
	}
}
