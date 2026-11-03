package daemon_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/config"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine/fakemachine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// The test binary is also the fake agent and its usage command: the
// machine runs it with one of these words as its first argument.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "fake-agent":
			os.Exit(fakeAgent())
		case "fake-echo":
			_, _ = io.Copy(os.Stdout, os.Stdin)
			os.Exit(0)
		case "fake-acp-agent":
			os.Exit(fakeACPAgent())
		case "fake-usage":
			os.Exit(fakeUsage())
		}
	}
	code := m.Run()
	if sortieBinDir != "" {
		_ = os.RemoveAll(sortieBinDir)
	}
	os.Exit(code)
}

// fakeAgent reads a prompt line, answers on standard output, makes one
// model request through the egress proxy and one tool call through
// the MCP forwarder, commits in its workspace, and exits. Its mode
// comes from AGENT_MODE: "work" (default), or "sleep", which answers
// and then waits to be killed.
func fakeAgent() int {
	in := bufio.NewReader(os.Stdin)
	line, err := in.ReadString('\n')
	if err != nil {
		fmt.Fprintln(os.Stderr, "no prompt:", err)
		return 3
	}
	fmt.Printf("agent got: %s", line)
	// What the guest process holds, for the token check.
	env := strings.Join(os.Environ(), "\n") + "\n" + strings.Join(os.Args, " ")
	_ = os.WriteFile("guest-env.txt", []byte(env), 0o600)
	if os.Getenv("AGENT_MODE") == "sleep" {
		fmt.Println("sleeping")
		time.Sleep(time.Hour)
		return 0
	}
	// hold-first: the first start in a workspace holds its turn until
	// its input ends, as an agent mid-turn when the control plane
	// restarts; a later start works.
	if os.Getenv("AGENT_MODE") == "hold-first" {
		if _, err := os.Stat("held.txt"); errors.Is(err, os.ErrNotExist) {
			_ = os.WriteFile("held.txt", []byte("held\n"), 0o600)
			fmt.Println("holding")
			_, _ = io.Copy(io.Discard, in)
			return 9
		}
	}
	// AGENT_STEPS: steps the agent reports, one per AGENT_DELAY, as a
	// vm chain writes its steps to its step file.
	if steps := os.Getenv("AGENT_STEPS"); steps != "" {
		d, _ := time.ParseDuration(os.Getenv("AGENT_DELAY"))
		for s := range strings.SplitSeq(steps, ",") {
			f, err := os.OpenFile(os.Getenv("SORTIE_STEP_FILE"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
			if err != nil {
				fmt.Fprintln(os.Stderr, "step file:", err)
				return 8
			}
			_, _ = fmt.Fprintln(f, s)
			_ = f.Close()
			time.Sleep(d)
		}
	} else if d, err := time.ParseDuration(os.Getenv("AGENT_DELAY")); err == nil {
		time.Sleep(d)
	}
	if err := modelRequest(); err != nil {
		fmt.Fprintln(os.Stderr, "model request:", err)
		return 4
	}
	if err := toolCall(); err != nil {
		fmt.Fprintln(os.Stderr, "tool call:", err)
		return 5
	}
	fmt.Fprintln(os.Stderr, "agent: working")
	if err := os.WriteFile("work.txt", []byte("done by the agent\n"), 0o600); err != nil {
		return 6
	}
	if err := os.MkdirAll(".sortie", 0o700); err != nil {
		return 6
	}
	if err := os.WriteFile(".sortie/pr.json", []byte(testPR), 0o600); err != nil {
		return 6
	}
	gitSteps := [][]string{{"add", "-A"}, {"-c", "user.name=agent", "-c", "user.email=agent@example.invalid", "commit", "-q", "-m", "agent work"}}
	switch os.Getenv("AGENT_GIT") {
	case "none":
		gitSteps = nil
	case "no-branch":
		gitSteps = [][]string{{"checkout", "-q", "--detach"}, {"branch", "-q", "-D", os.Getenv("SORTIE_ISSUE_BRANCH")}}
	}
	for _, args := range gitSteps {
		cmd := exec.Command("git", args...)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "git:", err)
			return 7
		}
	}
	fmt.Println("done")
	if code, err := strconv.Atoi(os.Getenv("AGENT_EXIT")); err == nil {
		return code
	}
	return 0
}

// testPR is the .sortie/pr.json the fake agent writes.
const testPR = `{"title": "Agent work", "body": "Adds work.txt."}`

// modelRequest is an HTTPS model call as a client makes it: CONNECT
// through the proxy, then HTTP inside the tunnel (the fake egress flow
// answers in clear).
func modelRequest() error {
	pu, err := url.Parse(os.Getenv("HTTPS_PROXY"))
	if err != nil {
		return err
	}
	c, err := net.DialTimeout("tcp", pu.Host, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(c, "CONNECT model.example:443 HTTP/1.1\r\nHost: model.example:443\r\nProxy-Authorization: Bearer guest-made-up\r\n\r\n"); err != nil {
		return err
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("CONNECT: %s", resp.Status)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	if _, err := fmt.Fprintf(c, "POST /v1/messages HTTP/1.1\r\nHost: model.example\r\nContent-Length: %d\r\n\r\n%s", len(body), body); err != nil {
		return err
	}
	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model: %s", resp.Status)
	}
	return nil
}

func toolCall() error {
	resp, err := http.Post(os.Getenv(daemon.MCPURLEnv), "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tracker_api"}}`))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || !bytes.Contains(b, []byte("tool ok")) {
		return fmt.Errorf("mcp: %s: %s", resp.Status, b)
	}
	return nil
}

func fakeUsage() int {
	switch os.Getenv("USAGE_MODE") {
	case "fail":
		fmt.Fprintln(os.Stderr, "usage: no session data")
		return 3
	case "garbled":
		fmt.Println("tokens: lots")
		return 0
	}
	fmt.Println(`{"input_tokens":1200,"output_tokens":300,"cache_read_tokens":50,"model":"claude-test"}`)
	return 0
}

// fakeProbe is a host with fixed figures.
type fakeProbe struct {
	mu sync.Mutex
	s  hostinfo.Snapshot
}

func (p *fakeProbe) Snapshot(string) (hostinfo.Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.s, nil
}

// syncBuffer is a log sink safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type harness struct {
	t       *testing.T
	fake    *fakecp.Fake
	backend *fakemachine.Backend
	probe   *fakeProbe
	d       *daemon.Daemon
	cfg     config.Config
	logs    *syncBuffer
	cancel  context.CancelFunc
	done    chan struct{}
}

type setup struct {
	reserveMem, floorDisk float64
	smallest              config.Size
	host                  hostinfo.Snapshot
	leaseMS               int64
	usage                 []string
	noUsage               bool
	noStart               bool
	// backend and pack replace the fake machines with a real backend.
	backend machine.Backend
	pack    string
	// images is the configuration's smolvm.images.
	images map[string]any
	// nested, when set, is the configuration's smolvm.nested.
	nested any
	// autoSlots leaves run_slots out, so the slots follow the host.
	autoSlots bool
}

func defaults() setup {
	return setup{reserveMem: 16, floorDisk: 10, leaseMS: 600,
		host: hostinfo.Snapshot{OS: "linux", Arch: "amd64", CPUs: 8, MemAvailableGiB: 40, DiskAvailableGiB: 200}}
}

const testImage = "ghcr.io/example/agent:1"

func newHarness(t *testing.T, s setup) *harness {
	t.Helper()
	fake := fakecp.New()
	fake.LeaseMS = s.leaseMS
	t.Cleanup(fake.Close)
	// A short state directory keeps Unix socket paths within their limit.
	state, err := os.MkdirTemp("", "wd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pack := s.pack
	if pack == "" {
		pack = "/packs/agent.smolmachine"
	}
	usage := s.usage
	if usage == nil {
		usage = []string{self, "fake-usage"}
	}
	doc := map[string]any{
		"worker_id":     "taichi",
		"control_plane": map[string]any{"url": fake.URL()},
		"run_slots":     1,
		"reserve":       map[string]any{"mem_gib": s.reserveMem},
		"floor":         map[string]any{"disk_gib": s.floorDisk},
		"smallest_job":  s.smallest,
		"state_dir":     state,
		"smolvm":        map[string]any{"packs": map[string]string{testImage: pack}, "images": s.images, "nested": s.nested},
		"log_level":     "debug",
	}
	if s.autoSlots {
		delete(doc, "run_slots")
	}
	if !s.noUsage {
		doc["usage_commands"] = map[string][]string{"*": usage}
	}
	raw, _ := json.Marshal(doc)
	cfg, err := config.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	probe := &fakeProbe{s: s.host}
	backend := &fakemachine.Backend{Dir: t.TempDir()}
	var mb machine.Backend = backend
	if s.backend != nil {
		mb = s.backend
	}
	d, err := daemon.New(daemon.Options{Config: cfg, Probe: probe, Backend: mb,
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, fake: fake, backend: backend, probe: probe, d: d, cfg: cfg, logs: logs, done: make(chan struct{})}
	if !s.noStart {
		h.start()
	}
	t.Cleanup(func() {
		h.stop()
		if t.Failed() {
			t.Logf("daemon log:\n%s", logs.String())
		}
	})
	return h
}

func (h *harness) start() {
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() {
		defer close(h.done)
		if err := h.d.Run(ctx); err != nil {
			h.t.Errorf("Run: %v", err)
		}
	}()
}

// restartDaemon stops the daemon as systemd stops it and starts a new
// one on the same host: the same configuration, state directory and
// machines.
func (h *harness) restartDaemon() {
	h.t.Helper()
	h.stop()
	h.startNewDaemon()
}

// startNewDaemon starts a new daemon on the host, after h.stop.
func (h *harness) startNewDaemon() {
	h.t.Helper()
	d, err := daemon.New(daemon.Options{Config: h.cfg, Probe: h.probe, Backend: h.backend,
		Log: slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	if err != nil {
		h.t.Fatal(err)
	}
	h.d, h.done = d, make(chan struct{})
	h.start()
}

func (h *harness) stop() {
	if h.cancel == nil {
		return
	}
	h.cancel()
	select {
	case <-h.done:
	case <-time.After(30 * time.Second):
		h.t.Error("daemon did not stop")
	}
	h.cancel = nil
}

// waitFor polls cond until it holds or the timeout passes.
func (h *harness) waitFor(what string, timeout time.Duration, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; events: %s", what, h.describe())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (h *harness) describe() string {
	var b strings.Builder
	for _, e := range h.fake.Events() {
		fmt.Fprintf(&b, "\n  %s %s#%d %d", e.Kind, e.Job, e.Attempt, e.Status)
	}
	return b.String()
}

// seedBundle builds a repository with branch on top of main, checked
// out, and returns its bundle.
func seedBundle(t *testing.T, branch string) []byte {
	t.Helper()
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=seed", "-c", "user.email=seed@example.invalid", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git("add", "README.md")
	git("commit", "-q", "-m", "seed")
	git("checkout", "-q", "-b", branch)
	git("bundle", "create", "-q", "seed.bundle", "HEAD", "main", branch)
	b, err := os.ReadFile(filepath.Join(dir, "seed.bundle"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

//nolint:unparam // every scenario names ONS-7, as the spec does
func (h *harness) offer(id string, attempt int, env map[string]string) {
	h.offerJob(id, attempt, env, nil)
}

// offerJob offers a job as offer does, adjusted by adjust.
func (h *harness) offerJob(id string, attempt int, env map[string]string, adjust func(*fakecp.Job)) {
	self, _ := os.Executable()
	if env == nil {
		env = map[string]string{}
	}
	env["SORTIE_ISSUE_IDENTIFIER"] = id
	env["SORTIE_ISSUE_BRANCH"] = "sortie/" + id
	env["GIT_CONFIG_GLOBAL"] = "/dev/null"
	j := fakecp.Job{
		ID: id, Attempt: attempt,
		Requires: protocol.Requires{Capabilities: []string{"smolvm"}, CPU: 2, MemGiB: 4, DiskGiB: 10},
		Run: protocol.Run{Backend: "smolvm", Image: testImage, Command: self, Args: []string{"fake-agent"}, Env: env,
			Credentials: []protocol.RunCredential{{Name: "anthropic", Env: "ANTHROPIC_API_KEY", Placeholder: "sortie-credential-anthropic", Hosts: []string{"api.anthropic.com"}}}},
		Seed:       seedBundle(h.t, "sortie/"+id),
		StdioInput: []string{"hello agent\n"},
	}
	if adjust != nil {
		adjust(&j)
	}
	h.fake.Offer(j)
}

//nolint:unparam // every scenario names ONS-7, as the spec does
func (h *harness) reports(id string, attempt int) []protocol.Report {
	var out []protocol.Report
	for _, e := range h.fake.Events() {
		if e.Kind == "report" && e.Job == id && e.Attempt == attempt {
			var r protocol.Report
			_ = json.Unmarshal(e.Body, &r)
			out = append(out, r)
		}
	}
	return out
}

//nolint:unparam // every scenario names ONS-7, as the spec does
func (h *harness) machine(id string, attempt int) *fakemachine.Machine {
	return h.backend.Machine(daemon.MachineName(id, attempt))
}

var _ machine.Backend = (*fakemachine.Backend)(nil)
