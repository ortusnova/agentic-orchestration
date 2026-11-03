package daemon_test

// The daemon against the real Sortie data plane: Sortie's plan-bench
// binary under environment.kind remote, with a file tracker and a local
// forge (a bare repository and a fake Gitea API), reached through the
// stage-1 edge (dev/stage0/edge), whose egress goes to a fake nono. The
// agent is this test binary speaking the Agent Client Protocol.

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/config"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine/fakemachine"
)

const (
	e2eControlToken = "control-token-of-the-e2e"
	e2eNonoPass     = "nono-pass-of-the-e2e"
	e2eForgeToken   = "forge-token-of-the-e2e"
	e2eProject      = "ortusnova/app"
)

// sortieBins are the Sortie and edge binaries, built once per test run
// into sortieBinDir.
var (
	sortieBinOnce sync.Once
	sortieBinDir  string
	sortieBinErr  error
	sortieBinSkip string
)

// sortieSource finds Sortie's source: WORKER_DAEMON_SORTIE_SRC, or the
// repository's sortie/ submodule, which tracks Sortie's plan-bench.
func sortieSource() (string, error) {
	if dir := os.Getenv("WORKER_DAEMON_SORTIE_SRC"); dir != "" {
		return filepath.Abs(dir)
	}
	return filepath.Abs("../../../sortie")
}

func buildSortieBins() {
	src, err := sortieSource()
	if err != nil {
		sortieBinErr = err
		return
	}
	if _, err := os.Stat(filepath.Join(src, "internal", "dataplane", "plane.go")); err != nil {
		sortieBinSkip = fmt.Sprintf("Sortie's source with its data plane is not at %s: run git submodule update --init sortie, or set WORKER_DAEMON_SORTIE_SRC", src)
		return
	}
	edge, err := filepath.Abs("../../../dev/stage0/edge")
	if err != nil {
		sortieBinErr = err
		return
	}
	sortieBinDir, err = os.MkdirTemp("", "wd-e2e-bin")
	if err != nil {
		sortieBinErr = err
		return
	}
	for _, b := range []struct{ dir, pkg, out string }{{src, "./cmd/sortie", "sortie"}, {edge, ".", "stage0-edge"}} {
		cmd := exec.Command("go", "build", "-o", filepath.Join(sortieBinDir, b.out), b.pkg)
		cmd.Dir = b.dir
		cmd.Env = os.Environ()
		if b.out == "sortie" {
			// Sortie is not in go.work; it builds from its own go.mod.
			cmd.Env = append(cmd.Env, "GOWORK=off")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			sortieBinErr = fmt.Errorf("building %s in %s: %v: %s", b.out, b.dir, err, out)
			return
		}
	}
}

func sortieBins(t *testing.T) (sortie, edge string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipped in -short: builds and runs Sortie")
	}
	sortieBinOnce.Do(buildSortieBins)
	if sortieBinSkip != "" {
		if os.Getenv("WORKER_DAEMON_E2E_NO_SKIP") != "" {
			t.Fatal(sortieBinSkip)
		}
		t.Skip("skipped: " + sortieBinSkip)
	}
	if sortieBinErr != nil {
		t.Fatal(sortieBinErr)
	}
	return filepath.Join(sortieBinDir, "sortie"), filepath.Join(sortieBinDir, "stage0-edge")
}

// fakeACPAgent speaks the Agent Client Protocol on its standard streams,
// one JSON-RPC message per line. Each prompt makes one model request
// through the egress proxy, writes and commits work.txt, writes
// .sortie/pr.json, and ends the turn. It exits when its input closes.
// It reports no usage and streams no output on the protocol, so the
// worker's usage reports alone decide how Sortie accounts for the run.
// With the argument hold, it answers no prompt: the turn lasts until
// the agent's input closes.
func fakeACPAgent() int {
	hold := len(os.Args) > 2 && os.Args[2] == "hold"
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64<<10), 8<<20)
	out := json.NewEncoder(os.Stdout)
	answer := func(id json.RawMessage, result any) {
		_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	}
	for in.Scan() {
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(in.Bytes(), &msg) != nil || msg.Method == "" {
			continue
		}
		fmt.Fprintln(os.Stderr, "fake-acp-agent:", msg.Method)
		switch {
		case msg.Method == "initialize":
			answer(msg.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{}, "authMethods": []any{},
				"agentInfo": map[string]any{"name": "fake-acp-agent", "version": "1"}})
		case msg.Method == "session/new":
			answer(msg.ID, map[string]any{"sessionId": "fake-session-1"})
		case msg.Method == "session/prompt" && hold:
			fmt.Fprintln(os.Stderr, "fake-acp-agent: holding the turn")
		case msg.Method == "session/prompt":
			if err := acpWork(); err != nil {
				fmt.Fprintln(os.Stderr, "fake-acp-agent:", err)
				_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32603, "message": err.Error()}})
				continue
			}
			answer(msg.ID, map[string]any{"stopReason": "end_turn"})
		case len(msg.ID) > 0:
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
	}
	return 0
}

func acpWork() error {
	if err := modelRequest(); err != nil {
		return fmt.Errorf("model request: %w", err)
	}
	if err := os.WriteFile("work.txt", []byte("done by the agent\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(".sortie", 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(".sortie/pr.json", []byte(testPR), 0o600); err != nil {
		return err
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=agent", "-c", "user.email=agent@example.invalid", "commit", "-q", "-m", "agent work"}} {
		cmd := exec.Command("git", args...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %v: %w", args, err)
		}
	}
	return nil
}

// fakeNono is nono's proxy as the edge reaches it: CONNECT with the
// edge's Basic credential, then, inside the tunnel, a model API in
// clear.
type fakeNono struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
}

func newFakeNono(t *testing.T) *fakeNono {
	n := &fakeNono{}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("nono:"+e2eNonoPass))
	n.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Header.Get("Proxy-Authorization") != want {
			http.Error(w, "nono: refused", http.StatusProxyAuthRequired)
			return
		}
		conn, rw, err := http.NewResponseController(w).Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
		_ = rw.Flush()
		for {
			req, err := http.ReadRequest(rw.Reader)
			if err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, req.Body)
			n.mu.Lock()
			n.requests++
			n.mu.Unlock()
			body := `{"model":"fake","usage":{"input_tokens":3}}`
			if _, err := fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
				return
			}
		}
	}))
	t.Cleanup(n.Close)
	return n
}

func (n *fakeNono) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.requests
}

// pullRequest is one pull request the fake forge holds.
type pullRequest struct {
	Number int    `json:"number"`
	Head   string `json:"head"`
	Base   string `json:"base"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

// fakeGitea is the forge's API: enough of Gitea's for Sortie to find or
// open a pull request.
type fakeGitea struct {
	*httptest.Server
	mu    sync.Mutex
	pulls []pullRequest
	paths []string
}

func newFakeGitea(t *testing.T) *fakeGitea {
	g := &fakeGitea{}
	repo := "/api/v1/repos/" + e2eProject
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.paths = append(g.paths, r.Method+" "+r.URL.RequestURI())
		g.mu.Unlock()
		if r.Header.Get("Authorization") != "token "+e2eForgeToken && r.Header.Get("Authorization") != "Bearer "+e2eForgeToken {
			http.Error(w, `{"message":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/user":
			_, _ = io.WriteString(w, `{"id":1,"login":"sortie"}`)
		case r.Method == http.MethodGet && r.URL.Path == repo:
			_, _ = io.WriteString(w, `{"id":1,"full_name":"`+e2eProject+`","default_branch":"main","permissions":{"admin":false,"push":true,"pull":true}}`)
		case r.Method == http.MethodGet && r.URL.Path == repo+"/pulls":
			g.mu.Lock()
			var out []map[string]any
			for _, p := range g.pulls {
				out = append(out, g.render(p))
			}
			g.mu.Unlock()
			if out == nil {
				out = []map[string]any{}
			}
			_ = json.NewEncoder(w).Encode(out)
		case r.Method == http.MethodPost && r.URL.Path == repo+"/pulls":
			var p pullRequest
			if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
				http.Error(w, `{"message":"bad body"}`, http.StatusUnprocessableEntity)
				return
			}
			g.mu.Lock()
			p.Number = len(g.pulls) + 1
			g.pulls = append(g.pulls, p)
			body := g.render(p)
			g.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(body)
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"not found"}`)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func (g *fakeGitea) render(p pullRequest) map[string]any {
	return map[string]any{
		"number": p.Number, "html_url": fmt.Sprintf("%s/%s/pulls/%d", g.URL, e2eProject, p.Number), "state": "open",
		"title": p.Title, "body": p.Body,
		"head": map[string]any{"ref": p.Head, "label": p.Head, "repo": map[string]any{"full_name": e2eProject}},
		"base": map[string]any{"ref": p.Base, "label": p.Base, "repo": map[string]any{"full_name": e2eProject}},
	}
}

func (g *fakeGitea) pullRequests() []pullRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]pullRequest(nil), g.pulls...)
}

// process is a started binary whose output is kept for the failure log.
type process struct {
	name string
	cmd  *exec.Cmd
	out  *syncBuffer
}

func start(t *testing.T, name string, cmd *exec.Cmd) *process {
	t.Helper()
	p := &process{name: name, cmd: cmd, out: &syncBuffer{}}
	cmd.Stdout, cmd.Stderr = p.out, p.out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Logf("%s output:\n%s", name, p.out.String())
		}
	})
	return p
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=seed", "-c", "user.email=seed@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// sortieRun is one attempt of ONS-7 run by Sortie on the daemon.
type sortieRun struct {
	t        *testing.T
	sortie   *process
	edge     *process
	daemon   *syncBuffer
	backend  *fakemachine.Backend
	forge    *fakeGitea
	nono     *fakeNono
	upstream string
	api      string
	dir      string
}

type sortieSetup struct {
	// usage is the daemon's usage command; nil configures none.
	usage []string
	// hold makes the agent hold its turn until its input closes.
	hold bool
}

const e2eWorkflow = `---
tracker:
  kind: file
  active_states: [Todo]
  terminal_states: [Done]
  handoff_state: In Review
file:
  path: {{dir}}/issues.json
gitea:
  endpoint: {{forge}}
  api_key: ` + e2eForgeToken + `
server:
  port: {{sortie_port}}
  control_token: ` + e2eControlToken + `
polling:
  interval_ms: 500
workspace:
  root: {{dir}}/workspaces
db_path: {{dir}}/sortie.db
agent:
  kind: agent-client-protocol
  command: {{agent}} fake-acp-agent{{agent_args}}
  max_turns: 1
  read_timeout_ms: 30000
environment:
  kind: remote
  remote:
    image: ` + testImage + `
    backend: smolvm
    requires: {cpu: 1, mem_gib: 1, disk_gib: 1}
    flows:
      stdio: {url: "http://127.0.0.1:{{edge_port}}/stdio"}
      egress: {url: "http://127.0.0.1:{{edge_port}}"}
      mcp: {url: "http://127.0.0.1:{{edge_port}}/mcp"}
      bundles: {url: "http://127.0.0.1:{{edge_port}}/bundles"}
      usage: {url: "http://127.0.0.1:{{edge_port}}/usage"}
    lease_ttl_ms: 6000
    repository:
      url: {{dir}}/upstream.git
      base: main
      provider: gitea
      project: ` + e2eProject + `
---
Work on {{ .issue.identifier }}: {{ .issue.title }}
`

func newSortieRun(t *testing.T, s sortieSetup) *sortieRun {
	t.Helper()
	sortieBin, edgeBin := sortieBins(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	r := &sortieRun{t: t, forge: newFakeGitea(t), nono: newFakeNono(t), upstream: filepath.Join(dir, "upstream.git"), dir: dir}

	// The local forge's repository: main with one commit.
	runGit(t, dir, "init", "-q", "--bare", "upstream.git")
	work := filepath.Join(dir, "work")
	runGit(t, dir, "clone", "-q", r.upstream, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("app\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, work, "add", "README.md")
	runGit(t, work, "commit", "-q", "-m", "seed")
	runGit(t, work, "push", "-q", "origin", "main")

	issues := `[{"id": "id-ONS-7", "identifier": "ONS-7", "title": "Speak the wire", "description": "One attempt over the wire.",
  "priority": 1, "state": "Todo", "labels": [], "url": "http://tracker.invalid/ONS-7"}]`
	sortiePort, edgePort := freePort(t), freePort(t)
	r.api = "http://127.0.0.1:" + strconv.Itoa(sortiePort)
	agentArgs := ""
	if s.hold {
		agentArgs = " hold"
	}
	wf := strings.NewReplacer("{{dir}}", dir, "{{forge}}", r.forge.URL, "{{sortie_port}}", strconv.Itoa(sortiePort),
		"{{edge_port}}", strconv.Itoa(edgePort), "{{agent}}", self, "{{agent_args}}", agentArgs).Replace(e2eWorkflow)
	nonoAddr := strings.TrimPrefix(r.nono.URL, "http://")
	edgeCfg, _ := json.Marshal(map[string]any{
		"sortie": r.api, "control_token_file": filepath.Join(dir, "sortie-token"),
		"workers": []map[string]any{{"name": "taichi", "port": edgePort}},
		"flows": map[string]string{
			"stdio": r.api + "/api/v1/flows/stdio", "mcp": r.api + "/mcp",
			"bundles": r.api + "/api/v1/flows/bundles", "usage": r.api + "/api/v1/flows/usage",
		},
		"egress": nonoAddr, "egress_pass_file": filepath.Join(dir, "nono-pass"),
	})
	for name, content := range map[string]string{
		"issues.json": issues, "WORKFLOW.md": wf, "sortie-token": e2eControlToken + "\n", "nono-pass": e2eNonoPass + "\n", "edge.json": string(edgeCfg),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(sortieBin, "--log-level", "debug", "--port", strconv.Itoa(sortiePort), filepath.Join(dir, "WORKFLOW.md"))
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	r.sortie = start(t, "sortie", cmd)
	r.waitFor("Sortie to be ready", 60*time.Second, func() bool {
		resp, err := http.Get(r.api + "/readyz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
	r.edge = start(t, "edge", exec.Command(edgeBin, "-debug", "-config", filepath.Join(dir, "edge.json")))
	r.waitFor("the edge to listen", 30*time.Second, func() bool {
		c, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(edgePort))
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})

	// The worker daemon, on fake machines, dials the edge only.
	state, err := os.MkdirTemp("", "wd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	doc := map[string]any{
		"worker_id":     "taichi",
		"control_plane": map[string]any{"url": "http://127.0.0.1:" + strconv.Itoa(edgePort)},
		"run_slots":     1,
		"reserve":       map[string]any{"mem_gib": 1},
		"floor":         map[string]any{"disk_gib": 1},
		"state_dir":     state,
		"smolvm":        map[string]any{"packs": map[string]string{testImage: "/packs/agent.smolmachine"}},
		"log_level":     "debug",
	}
	if s.usage != nil {
		doc["usage_commands"] = map[string][]string{"*": s.usage}
	}
	raw, _ := json.Marshal(doc)
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.daemon = &syncBuffer{}
	r.backend = &fakemachine.Backend{Dir: t.TempDir()}
	probe := &fakeProbe{s: hostinfo.Snapshot{OS: "linux", Arch: "amd64", CPUs: 8, MemAvailableGiB: 32, DiskAvailableGiB: 100}}
	d, err := daemon.New(daemon.Options{Config: cfg, Probe: probe, Backend: r.backend,
		Log: slog.New(slog.NewTextHandler(r.daemon, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := d.Run(ctx); err != nil {
			t.Errorf("daemon: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		if t.Failed() {
			t.Logf("daemon log:\n%s", r.daemon.String())
		}
	})
	return r
}

func (r *sortieRun) waitFor(what string, timeout time.Duration, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			r.t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// run is one finished run as GET /api/v1/runs serves it.
type run struct {
	Status         string  `json:"status"`
	Error          *string `json:"error"`
	Attempt        int     `json:"attempt"`
	TokensMeasured bool    `json:"tokens_measured"`
	Unaccounted    int     `json:"unaccounted_turns"`
	Tokens         struct {
		Input      *int64 `json:"input_tokens"`
		Output     *int64 `json:"output_tokens"`
		CacheRead  *int64 `json:"cache_read_tokens"`
		CacheWrite *int64 `json:"cache_write_tokens"`
	} `json:"tokens"`
}

// finishedRun waits for Sortie's first finished run of ONS-7.
func (r *sortieRun) finishedRun() run {
	r.t.Helper()
	var got []run
	r.waitFor("Sortie to finish a run of ONS-7", 2*time.Minute, func() bool {
		req, _ := http.NewRequest(http.MethodGet, r.api+"/api/v1/runs?issue=ONS-7", nil)
		req.Header.Set("Authorization", "Bearer "+e2eControlToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		var page struct {
			Runs []run `json:"runs"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&page) != nil {
			return false
		}
		got = page.Runs
		return len(got) > 0
	})
	return got[len(got)-1]
}

// admitted returns the flows the edge admitted, from its debug log.
func (r *sortieRun) admitted() map[string]int {
	out := map[string]int{}
	re := regexp.MustCompile(`msg="flow admitted" worker=taichi flow=(\w+) job=ONS-7 attempt=1`)
	for _, m := range re.FindAllStringSubmatch(r.edge.out.String(), -1) {
		out[m[1]]++
	}
	if strings.Contains(r.edge.out.String(), `msg="egress tunnel" worker=taichi job=ONS-7 attempt=1`) {
		out["egress"]++
	}
	return out
}

// Scenario: One attempt over the wire. A worker runs ONS-7 attempt 1
// against Sortie's data plane, through the edge: it fetches the seed,
// carries the agent's stdio in frames ending with X, posts the result
// with pr, and posts usage, each with its own token, and Sortie opens the
// pull request.
func TestOneAttemptOverTheWire(t *testing.T) {
	self, _ := os.Executable()
	r := newSortieRun(t, sortieSetup{usage: []string{self, "fake-usage"}})
	got := r.finishedRun()
	if got.Status != "succeeded" || got.Attempt != 1 {
		t.Fatalf("run = %+v, want attempt 1 succeeded", got)
	}

	// Sortie opened the pull request from the issue branch, with the
	// agent's .sortie/pr.json, on the local forge.
	var prs []pullRequest
	r.waitFor("the pull request", 30*time.Second, func() bool { prs = r.forge.pullRequests(); return len(prs) > 0 })
	if len(prs) != 1 || prs[0].Head != "sortie/ONS-7" || prs[0].Base != "main" || prs[0].Title != "Agent work" || prs[0].Body != "Adds work.txt." {
		t.Errorf("pull requests = %+v, want one from sortie/ONS-7 to main with the agent's title and body", prs)
	}
	log := runGit(t, r.upstream, "log", "--format=%s", "refs/heads/sortie/ONS-7")
	if log != "agent work\nseed" {
		t.Errorf("the forge's sortie/ONS-7 = %q, want the agent's commit on the seed", log)
	}
	files := runGit(t, r.upstream, "show", "--name-only", "--format=", "refs/heads/sortie/ONS-7")
	if files != "work.txt" {
		t.Errorf("the agent's commit holds %q, want work.txt", files)
	}

	// Sortie recorded the usage the worker posted, measured.
	if !got.TokensMeasured || got.Unaccounted != 0 || got.Tokens.Input == nil || *got.Tokens.Input != 1200 ||
		*got.Tokens.Output != 300 || *got.Tokens.CacheRead != 50 {
		b, _ := json.Marshal(got.Tokens)
		t.Errorf("run tokens = %s, measured %v, unaccounted %d; want 1200 in, 300 out, 50 cache read, measured",
			b, got.TokensMeasured, got.Unaccounted)
	}

	// Every flow went through the edge, each admitted by its own token:
	// the edge admits a request only when its token's grant names the
	// flow of the path.
	admitted := r.admitted()
	for _, flow := range []string{"bundles", "stdio", "usage", "egress"} {
		if admitted[flow] == 0 {
			t.Errorf("the edge admitted no %s request for ONS-7 attempt 1; admitted %v", flow, admitted)
		}
	}
	if admitted["bundles"] < 2 {
		t.Errorf("want the seed and the result on bundles, got %d requests", admitted["bundles"])
	}
	if strings.Contains(r.edge.out.String(), "flow refused") {
		t.Errorf("the edge refused a flow request:\n%s", r.edge.out.String())
	}
	if r.nono.count() == 0 {
		t.Error("the agent's model request did not reach nono")
	}

	// The daemon sent the exit last on the stream, returned the result
	// with pr before its report, and its report succeeded.
	logs := r.daemon.String()
	for _, want := range []string{`msg="agent exited" job=ONS-7 attempt=1 status=0`, `msg="usage reported" job=ONS-7 attempt=1 measured=true`,
		`msg="result returned" job=ONS-7 attempt=1 branch=sortie/ONS-7 bundle=true pr=true`, `msg=reported job=ONS-7 attempt=1 outcome=succeeded`} {
		if !strings.Contains(logs, want) {
			t.Errorf("the daemon's log lacks %q", want)
		}
	}
	if i, j := strings.Index(logs, `msg="result returned"`), strings.Index(logs, `msg=reported`); i < 0 || j < i {
		t.Error("the daemon reported before it returned the result")
	}
	if strings.Contains(r.sortie.out.String(), "stdio stream broke") {
		t.Error("Sortie saw the stdio stream break before the agent's exit")
	}
}

// Scenario: Unknown amount. A run made model requests and its usage
// cannot be read: the worker posts "measured": false, and Sortie records
// spend occurred, amount unknown.
func TestUnknownAmountOverTheWire(t *testing.T) {
	r := newSortieRun(t, sortieSetup{})
	got := r.finishedRun()
	if got.Status != "succeeded" {
		t.Fatalf("run = %+v, want succeeded", got)
	}
	if r.nono.count() == 0 {
		t.Fatal("the run made no model request")
	}
	if !strings.Contains(r.daemon.String(), `msg="usage reported" job=ONS-7 attempt=1 measured=false`) {
		t.Error(`the daemon did not post "measured": false`)
	}
	if got.Unaccounted < 1 || got.TokensMeasured {
		t.Errorf("run = measured %v, unaccounted turns %d; want spend occurred, amount unknown", got.TokensMeasured, got.Unaccounted)
	}
}

// Scenario: Sortie cancels a job over the wire. ONS-7 moves to a
// terminal state while its agent holds the turn on the worker, and
// Sortie cancels the run. The worker releases the job: its machine is
// deleted and no report of the attempt is accepted. A Sortie that
// answers 410 for a job it no longer wants makes the daemon log the
// release.
func TestACancelledJobIsReleasedOverTheWire(t *testing.T) {
	r := newSortieRun(t, sortieSetup{hold: true})
	r.waitFor("the agent to hold its turn", 60*time.Second, func() bool {
		return strings.Contains(r.daemon.String(), `msg="agent starting" job=ONS-7 attempt=1`) &&
			strings.Contains(r.sortie.out.String(), "turn started")
	})
	machines := r.backend.Machines()
	if len(machines) != 1 {
		t.Fatalf("machines %d, want ONS-7's one", len(machines))
	}
	issues := `[{"id": "id-ONS-7", "identifier": "ONS-7", "title": "Speak the wire", "description": "One attempt over the wire.",
  "priority": 1, "state": "Done", "labels": [], "url": "http://tracker.invalid/ONS-7"}]`
	if err := os.WriteFile(filepath.Join(r.dir, "issues.json"), []byte(issues), 0o600); err != nil {
		t.Fatal(err)
	}
	r.waitFor("the machine to be deleted", 60*time.Second, machines[0].Deleted)
	r.waitFor("the job to end", 30*time.Second, func() bool {
		return strings.Contains(r.daemon.String(), "not reporting") || strings.Contains(r.daemon.String(), "report refused")
	})
	logs := r.daemon.String()
	if strings.Contains(logs, `msg=reported job=ONS-7`) {
		t.Errorf("Sortie accepted a report of the cancelled attempt:\n%s", logs)
	}
	for line := range strings.SplitSeq(logs, "\n") {
		if strings.Contains(line, "no longer wants the job") {
			t.Log(line)
		}
	}
}
