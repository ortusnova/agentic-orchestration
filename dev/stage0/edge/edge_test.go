package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// agent-platform/stage-1-edge#A worker is named by the tunnel it uses,
// scenario taichi.
func TestTaichiIsNamedByItsTunnel(t *testing.T) {
	h := newHarness(t, "127.0.0.1:1", "unused")
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/workers/checkin", strings.NewReader(`{}`))
	req.Header.Set("X-Sortie-Worker-Identity", "p1")
	req.Header["x-sortie-worker-identity"] = []string{"mac"}
	status, _ := h.do("taichi", req)
	if status != http.StatusOK {
		t.Fatalf("check-in answered %d", status)
	}
	if got := h.sortie.identities; len(got) != 1 || !slices.Equal(got[0], []string{"taichi"}) {
		t.Fatalf("Sortie saw identities %q, want only taichi", got)
	}

	req, _ = http.NewRequest(http.MethodPost, "/api/v1/workers/poll", nil)
	h.do("mac", req)
	if got := h.sortie.identities[1]; !slices.Equal(got, []string{"mac"}) {
		t.Fatalf("mac's tunnel named %q", got)
	}
}

func TestIdentityHeaderIsSortiesSetting(t *testing.T) {
	var seen []string
	sortie := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = r.Header.Values("X-Worker") }))
	t.Cleanup(sortie.Close)
	cfg := Config{
		Sortie:           sortie.URL,
		ControlTokenFile: writeSecret(t, "sortie-token", controlToken),
		IdentityHeader:   "X-Worker",
		Flows:            map[string]string{"stdio": sortie.URL, "mcp": sortie.URL, "bundles": sortie.URL, "usage": sortie.URL},
		Egress:           "127.0.0.1:1",
		EgressPassFile:   writeSecret(t, "nono-pass", "p"),
	}
	e, err := New(cfg, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	port := httptest.NewServer(e.Handler("taichi"))
	t.Cleanup(port.Close)
	req, _ := http.NewRequest(http.MethodPost, port.URL+"/api/v1/workers/poll", nil)
	req.Header.Set("X-Worker", "p1")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if !slices.Equal(seen, []string{"taichi"}) {
		t.Fatalf("X-Worker is %q, want taichi", seen)
	}
}

func TestEdgeListensOnlyOnLoopback(t *testing.T) {
	h := newHarness(t, "127.0.0.1:1", "unused")
	var ports []Worker
	for _, name := range []string{"taichi", "mac"} {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		ports = append(ports, Worker{Name: name, Port: l.Addr().(*net.TCPAddr).Port})
		_ = l.Close()
	}
	h.edge.cfg.Workers = ports
	bound, err := h.edge.Listen(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range bound {
		addr := b.Listener.Addr().(*net.TCPAddr)
		if !addr.IP.IsLoopback() || addr.Port != ports[i].Port || b.Worker != ports[i].Name {
			t.Errorf("worker %s listens on %s", b.Worker, addr)
		}
		_ = b.Listener.Close()
	}
}

// agent-platform/stage-1-edge#Each flow is routed by path and admitted by
// its job token, scenario Wrong flow.
func TestWrongFlow(t *testing.T) {
	h := newHarness(t, "127.0.0.1:1", "unused")
	req, _ := http.NewRequest(http.MethodGet, "/bundles/jobs/ONS-7/attempts/1/seed", nil)
	req.Header.Set("Authorization", "Bearer "+h.sortie.mint("mcp"))
	status, _ := h.do("taichi", req)
	if status != http.StatusUnauthorized {
		t.Fatalf("a bundles request with an mcp token got %d, want 401", status)
	}
	if n := h.services["bundles"].hits(); n != 0 {
		t.Fatalf("the bundles service was reached %d times", n)
	}
}

func TestEachFlowIsRoutedByPath(t *testing.T) {
	h := newHarness(t, "127.0.0.1:1", "unused")
	for i, flow := range pathFlows {
		tok := h.sortie.mint(flow)
		req, _ := http.NewRequest(http.MethodPost, "/"+flow+"/jobs/ONS-7/attempts/1", strings.NewReader("x"))
		req.Header.Set("Authorization", "Bearer "+tok)
		status, body := h.do("taichi", req)
		if status != http.StatusOK || body != flow+" served /svc/jobs/ONS-7/attempts/1" {
			t.Errorf("%s: %d %q", flow, status, body)
			continue
		}
		total := 0
		for _, s := range h.services {
			total += s.hits()
		}
		if h.services[flow].hits() != 1 || total != i+1 {
			t.Errorf("%s reached another service", flow)
		}
		got := h.services[flow].reqs[0]
		if got.Header.Get("Authorization") != "Bearer "+tok || got.Header.Get(DefaultIdentityHeader) != "taichi" {
			t.Errorf("%s: the service got Authorization %q and identity %q", flow, got.Header.Get("Authorization"), got.Header.Get(DefaultIdentityHeader))
		}
	}
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/workers/jobs/ONS-7/attempts/1/heartbeat", nil)
	if status, _ := h.do("taichi", req); status != http.StatusOK || h.sortie.paths[0] != "/api/v1/workers/jobs/ONS-7/attempts/1/heartbeat" {
		t.Errorf("the worker API did not reach Sortie: %d %q", status, h.sortie.paths)
	}
}

func TestFlowRequestsTheEdgeRefuses(t *testing.T) {
	h := newHarness(t, "127.0.0.1:1", "unused")
	ended := h.sortie.mint("stdio")
	h.sortie.end(ended)
	cases := []struct {
		name, path, auth string
		want             int
	}{
		{"no token", "/stdio/x", "", http.StatusUnauthorized},
		{"not a bearer token", "/stdio/x", "Basic " + h.sortie.mint("stdio"), http.StatusUnauthorized},
		{"an ended token", "/stdio/x", "Bearer " + ended, http.StatusUnauthorized},
		{"an unknown token", "/usage/x", "Bearer made-up", http.StatusUnauthorized},
		{"the egress token on a path flow", "/mcp", "Bearer " + h.sortie.mint("egress"), http.StatusUnauthorized},
		{"Sortie's introspection", "/api/v1/tokens/introspect", "Bearer " + controlToken, http.StatusNotFound},
		{"Sortie's state", "/api/v1/state", "Bearer " + controlToken, http.StatusNotFound},
		{"an unknown flow", "/files/x", "Bearer " + h.sortie.mint("stdio"), http.StatusNotFound},
		{"a path that climbs out of the worker API", "/api/v1/workers/../tokens/introspect", "", http.StatusBadRequest},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(`{"token": "x"}`))
		req.URL.Path = c.path
		if c.auth != "" {
			req.Header.Set("Authorization", c.auth)
		}
		status, _ := h.do("taichi", req)
		if status != c.want {
			t.Errorf("%s: %d, want %d", c.name, status, c.want)
		}
	}
	for f, s := range h.services {
		if s.hits() != 0 {
			t.Errorf("the %s service was reached", f)
		}
	}
	if len(h.sortie.paths) != 0 {
		t.Errorf("Sortie's worker API was reached: %q", h.sortie.paths)
	}

	// While Sortie cannot answer, as while it restarts, nothing is
	// admitted, and the answer is 503, not 401: the worker dials again
	// and keeps its machine, where a 401 would tell it the job is gone.
	tok := h.sortie.mint("stdio")
	h.sortie.Close()
	req, _ := http.NewRequest(http.MethodGet, "/stdio/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if status, _ := h.do("taichi", req); status != http.StatusServiceUnavailable {
		t.Errorf("with Sortie down: %d, want 503", status)
	}
	if h.services["stdio"].hits() != 0 {
		t.Error("the stdio service was reached with Sortie down")
	}
	if status := h.connect("taichi", "api.anthropic.com:443", h.sortie.mint("egress")); status != http.StatusServiceUnavailable {
		t.Errorf("egress with Sortie down: %d, want 503", status)
	}
}

// agent-platform/stage-1-edge#nono holds the credentials from stage 1,
// scenario Model request, against a stand-in for nono (TestLiveNono runs
// the real one).
func TestModelRequest(t *testing.T) {
	ca := newTestCA(t)
	var gotAuth string
	anthropic := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth != "Bearer the-credential-on-p1" {
			http.Error(w, "invalid x-api-key", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, `{"data": []}`)
	})
	nono := startFakeNono(t, ca, anthropic)
	h := newHarness(t, nono.addr, nono.pass)

	agent := agentClient(h.ports["taichi"].URL, h.sortie.mint("egress"), ca.pool)
	req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+anthropicPlaceholder)
	resp, err := agent.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the model request answered %d; upstream saw Authorization %q", resp.StatusCode, gotAuth)
	}
	if !strings.Contains(h.log.String(), `msg="egress tunnel" worker=taichi job=ONS-7 attempt=1 host=api.anthropic.com:443`) {
		t.Errorf("the tunnel is not logged with its job:\n%s", h.log)
	}
}

// agent-platform/stage-1-edge#nono holds the credentials from stage 1,
// scenario A host not allowed: the refusal is recorded with the job and
// attempt.
func TestAHostNotAllowed(t *testing.T) {
	nono := startFakeNono(t, newTestCA(t), http.NotFoundHandler())
	h := newHarness(t, nono.addr, nono.pass)
	status := h.connect("taichi", "pastebin.com:443", h.sortie.mint("egress"))
	if status != http.StatusForbidden {
		t.Fatalf("CONNECT to a host not allowed answered %d, want nono's 403", status)
	}
	want := `msg="egress refused" worker=taichi job=ONS-7 attempt=1 host=pastebin.com:443 status=403`
	if !strings.Contains(h.log.String(), want) {
		t.Fatalf("the refusal is not recorded with its job and attempt; want %s in:\n%s", want, h.log)
	}
}

func TestEgressNeedsTheEgressToken(t *testing.T) {
	nono := startFakeNono(t, newTestCA(t), http.NotFoundHandler())
	h := newHarness(t, nono.addr, nono.pass)
	ended := h.sortie.mint("egress")
	h.sortie.end(ended)
	for name, tok := range map[string]string{"an mcp token": h.sortie.mint("mcp"), "an ended token": ended, "no token": ""} {
		status := h.connect("taichi", "api.anthropic.com:443", tok)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: %d, want 401", name, status)
		}
	}
	if n := nono.connectCount(); n != 0 {
		t.Fatalf("nono was reached %d times", n)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://api.anthropic.com/v1/models", nil)
	req.RequestURI = "http://api.anthropic.com/v1/models"
	rec := httptest.NewRecorder()
	h.edge.Handler("taichi").ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("a plain proxy request answered %d, want 405", rec.Code)
	}
}

func TestConfigValidate(t *testing.T) {
	for name, ws := range map[string][]Worker{
		"two names, one port": {{"taichi", 7701}, {"mac", 7701}},
		"one name twice":      {{"taichi", 7701}, {"taichi", 7702}},
		"a name with a space": {{"tai chi", 7701}},
		"no port":             {{"taichi", 0}},
	} {
		if err := (Config{Workers: ws}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Config{Workers: []Worker{{"taichi", 7701}, {"mac.lan", 7702}}}).Validate(); err != nil {
		t.Errorf("two workers: %v", err)
	}
}
