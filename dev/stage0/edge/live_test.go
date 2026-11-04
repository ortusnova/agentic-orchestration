package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveNono runs both nono scenarios against the pinned nono with the
// kit's egress profile and the credentials on p1, through the edge, with
// a fake Sortie for introspection. It reaches api.anthropic.com, so it
// runs only from dev/stage0/test-egress-live.sh, which sets
// STAGE0_EDGE_LIVE_NONO to the nono binary and the STAGE0_EGRESS_*
// credentials as run-nono.sh does.
func TestLiveNono(t *testing.T) {
	bin := os.Getenv("STAGE0_EDGE_LIVE_NONO")
	if bin == "" {
		t.Skip("STAGE0_EDGE_LIVE_NONO is unset; dev/stage0/test-egress-live.sh runs this on p1")
	}
	for _, v := range []string{"STAGE0_EGRESS_ANTHROPIC", "STAGE0_EGRESS_GITHUB", "STAGE0_EGRESS_GITHUB_BASIC"} {
		if os.Getenv(v) == "" {
			t.Fatalf("%s is unset", v)
		}
	}
	ca := newTestCA(t)
	certFile, keyFile := ca.files(t)
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
		"--profile", "../egress-profile.json", "--proxy-ca-cert", certFile, "--proxy-ca-key", keyFile) // #nosec G204 -- the test runner names nono
	cmd.Env = append(os.Environ(), "NONO_PROXY_PASS="+pass, "NONO_NO_UPDATE_CHECK=1")
	var out syncBuffer
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
	h := newHarness(t, addr, pass)

	t.Run("Model request", func(t *testing.T) {
		agent := agentClient(h.ports["taichi"].URL, h.sortie.mint("egress"), ca.pool)
		req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+anthropicPlaceholder)
		req.Header.Set("anthropic-version", "2023-06-01")
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
		resp, err := agent.Do(req)
		if err != nil {
			t.Fatalf("%v\nnono:\n%s", err, out.String())
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("api.anthropic.com answered %d: %s\nnono:\n%s", resp.StatusCode, body, out.String())
		}
	})

	// opencode's websearch (pack/opencode.json) calls Firecrawl's keyless
	// MCP; nono tunnels it without a credential.
	t.Run("Web search", func(t *testing.T) {
		agent := agentClient(h.ports["taichi"].URL, h.sortie.mint("egress"), nil)
		call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"firecrawl_search","arguments":{"query":"smolvm","limit":2}}}`
		req, _ := http.NewRequest(http.MethodPost, "https://mcp.firecrawl.dev/v2/mcp", strings.NewReader(call))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
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
		status := h.connect("taichi", "pastebin.com:443", h.sortie.mint("egress"))
		if status == http.StatusOK {
			t.Fatal("nono let the job reach pastebin.com")
		}
		want := "msg=\"egress refused\" worker=taichi job=ONS-7 attempt=1 host=pastebin.com:443 status=" + strconv.Itoa(status)
		if !strings.Contains(h.log.String(), want) {
			t.Fatalf("the refusal is not recorded with its job and attempt; want %s in:\n%s", want, h.log)
		}
	})
}
