package flows

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
)

// A plain proxied request reaches the egress flow with the job's token
// in place of whatever the guest sent.
func TestEgressPlainRequest(t *testing.T) {
	seen := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Proxy-Authorization") + " " + r.URL.String()
		_, _ = w.Write([]byte("ok"))
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	e := &Egress{Upstream: u, Token: secret.New("egress-token")}
	fwd := httptest.NewServer(e)
	defer fwd.Close()
	pu, _ := url.Parse(fwd.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	req, _ := http.NewRequest(http.MethodGet, "http://model.example/v1/models", nil)
	req.Header.Set("Proxy-Authorization", "Bearer guest-made-up")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(b) != "ok" {
		t.Fatalf("body %q", b)
	}
	if got := <-seen; got != "Bearer egress-token http://model.example/v1/models" {
		t.Errorf("upstream saw %q", got)
	}
	if e.Requests() != 1 {
		t.Errorf("requests = %d", e.Requests())
	}
	// Not a proxy request.
	resp, err = http.Get(fwd.URL + "/x")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("origin-form request = %d, want 400", resp.StatusCode)
	}
}

// A tool call to /p reaches {mcp}/jobs/{job}/attempts/{n}/p with the
// job's mcp token.
func TestMCPRewrite(t *testing.T) {
	seen := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Get("Authorization") + " " + r.URL.RequestURI()
	}))
	defer up.Close()
	j, err := New(&protocol.Offer{Job: protocol.Job{ID: "ONS-7", Attempt: 2}, Flows: map[string]protocol.FlowAccess{
		"stdio": {URL: "wss://edge/stdio"}, "egress": {URL: "https://egress:3128"}, "bundles": {URL: "https://edge/bundles"},
		"usage": {URL: "https://edge/usage"}, "mcp": {URL: up.URL + "/mcp", Token: secret.New("mcp-token")},
	}}, http.DefaultClient, nil)
	if err != nil {
		t.Fatal(err)
	}
	fwd := httptest.NewServer(&MCP{Job: j})
	defer fwd.Close()
	req, _ := http.NewRequest(http.MethodPost, fwd.URL+"/rpc?x=1", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer guest")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := <-seen; got != "Bearer mcp-token /mcp/jobs/ONS-7/attempts/2/rpc?x=1" {
		t.Errorf("upstream saw %q", got)
	}
}

func TestNewRefusesAMissingFlow(t *testing.T) {
	_, err := New(&protocol.Offer{Flows: map[string]protocol.FlowAccess{"stdio": {URL: "wss://e/s"}}}, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "egress") {
		t.Errorf("err = %v, want one naming the missing egress flow", err)
	}
}

// A CONNECT reaches an https egress flow as HTTP/1.1 even when the
// daemon's TLS configuration, which its control-plane client shares,
// already offers h2: an HTTP/2 transport adds h2 to the configuration
// it is given on its first request, and a front such as Pomerium then
// agrees h2 and cannot read the CONNECT, which the guest saw as 502.
func TestEgressConnectOverTLSWithSharedConfig(t *testing.T) {
	up := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.ProtoMajor != 1 {
			http.Error(w, "want an HTTP/1.1 CONNECT", http.StatusBadRequest)
			return
		}
		http.Error(w, "refused upstream", http.StatusForbidden)
	}))
	up.EnableHTTP2 = true
	up.StartTLS()
	defer up.Close()
	cfg := up.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	cfg.NextProtos = nil
	// What the daemon's control-plane client does with the shared
	// configuration on its first request.
	control := &http.Client{Transport: &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}}
	resp, err := control.Get(up.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Fatalf("the control client spoke %s, want HTTP/2 for this test", resp.Proto)
	}

	u, _ := url.Parse(up.URL)
	e := &Egress{Upstream: u, Token: secret.New("egress-token"), TLS: cfg}
	fwd := httptest.NewServer(e)
	defer fwd.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(fwd.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("CONNECT model.example:443 HTTP/1.1\r\nHost: model.example:443\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(string(b), "refused upstream") {
		t.Fatalf("CONNECT through the forwarder = %d %q, want the upstream's 403", resp.StatusCode, b)
	}
}
