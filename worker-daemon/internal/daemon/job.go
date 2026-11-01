package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/cp"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/flows"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/protocol"
)

// Guest loopback ports of the forwarders.
const (
	EgressGuestPort = 47801
	MCPGuestPort    = 47802
)

// MCPURLEnv names the variable that tells the agent where its tool
// server is.
const MCPURLEnv = "WORKER_MCP_URL"

type job struct {
	d     *Daemon
	offer protocol.Offer
	key   string
	flows *flows.Job
	log   *slog.Logger

	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}

	mu      sync.Mutex
	m       machine.Machine
	state   string
	servers []*flows.Server
	egress  *flows.Egress
}

func (j *job) id() string   { return j.offer.Job.ID }
func (j *job) attempt() int { return j.offer.Job.Attempt }

func (j *job) listeners() []net.Addr {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []net.Addr
	for _, s := range j.servers {
		out = append(out, s.Addr())
	}
	return out
}

func (j *job) lost() bool { return errors.Is(context.Cause(j.ctx), errLeaseLost) }

// heartbeats renews the lease every heartbeat_ms until the run ends. A
// 409 ends the attempt: the machine stops at once and nothing is
// reported.
func (j *job) heartbeats() {
	for {
		interval := j.d.heartbeatInterval()
		select {
		case <-j.done:
			return
		case <-time.After(interval):
		}
		ctx, cancel := context.WithTimeout(context.Background(), interval)
		_, err := j.d.cp.Heartbeat(ctx, j.id(), j.attempt())
		cancel()
		select {
		case <-j.done:
			return
		default:
		}
		switch {
		case errors.Is(err, cp.ErrLeaseLost):
			j.log.Warn("heartbeat answered 409: stopping the machine, sending no report")
			j.cancel(errLeaseLost)
			j.stopMachine()
			return
		case err != nil:
			j.log.Warn("heartbeat failed", slog.String("error", err.Error()))
		}
	}
}

// stopMachine stops the machine, if there is one, now.
func (j *job) stopMachine() {
	j.mu.Lock()
	m := j.m
	j.mu.Unlock()
	if m == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), j.d.heartbeatInterval())
	defer cancel()
	if err := m.Stop(ctx); err != nil {
		j.log.Error("stopping the machine failed", slog.String("error", err.Error()))
		return
	}
	j.mu.Lock()
	j.state = protocol.MachineStopped
	j.mu.Unlock()
}

func (j *job) run() {
	defer j.d.finished(j)
	defer close(j.done)
	dir := j.d.JobDir(j.id(), j.attempt())
	defer func() { _ = os.RemoveAll(dir) }()
	defer j.closeServers()

	outcome, reason := j.execute(dir)
	machineState := j.dispose()
	if j.lost() {
		j.log.Info("attempt ended by the control plane; not reporting", slog.String("machine", machineState))
		return
	}
	if errors.Is(context.Cause(j.ctx), errShutdown) {
		outcome, reason = protocol.OutcomeFailed, errShutdown.Error()
	}
	j.d.report(j.log, j.id(), j.attempt(), protocol.Report{Outcome: outcome, Reason: reason, Machine: machineState})
}

// dispose deletes the machine and returns the state it is left in.
func (j *job) dispose() string {
	j.mu.Lock()
	m, state := j.m, j.state
	j.mu.Unlock()
	if m == nil {
		return protocol.MachineDeleted
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := m.Delete(ctx); err != nil {
		j.log.Error("deleting the machine failed", slog.String("machine", m.Name()), slog.String("error", err.Error()))
		if state == protocol.MachineStopped {
			return state
		}
		if err := m.Stop(ctx); err == nil {
			return protocol.MachineStopped
		}
		return protocol.MachineRunning
	}
	return protocol.MachineDeleted
}

func (j *job) closeServers() {
	j.mu.Lock()
	servers := j.servers
	j.servers = nil
	j.mu.Unlock()
	for _, s := range servers {
		_ = s.Close()
	}
}

func failed(format string, args ...any) (string, string) {
	return protocol.OutcomeFailed, fmt.Sprintf(format, args...)
}

// execute runs the job and returns its outcome and reason.
func (j *job) execute(dir string) (string, string) {
	ctx := j.ctx
	share := filepath.Join(dir, "share")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return failed("job directory: %v", err)
	}
	if err := os.MkdirAll(share, 0o700); err != nil {
		return failed("job directory: %v", err)
	}
	// The guest user writes the result bundle here; the job directory
	// around it is this user's alone.
	if err := os.Chmod(share, 0o777); err != nil { // #nosec G302 -- holds no secret; its parent is 0700
		return failed("job directory: %v", err)
	}

	eg := j.flows.Flows[protocol.FlowEgress]
	j.egress = &flows.Egress{Upstream: eg.URL, Token: eg.Token, TLS: j.d.tls}
	mcp := &flows.MCP{Job: j.flows}
	forwards := []machine.Forward{
		{Name: protocol.FlowEgress, HostSocket: filepath.Join(dir, "egress.sock"), GuestPort: EgressGuestPort},
		{Name: protocol.FlowMCP, HostSocket: filepath.Join(dir, "mcp.sock"), GuestPort: MCPGuestPort},
	}
	handlers := map[string]http.Handler{protocol.FlowEgress: j.egress, protocol.FlowMCP: mcp}
	for _, f := range forwards {
		s, err := flows.Serve(f.HostSocket, handlers[f.Name])
		if err != nil {
			return failed("forwarder: %v", err)
		}
		j.mu.Lock()
		j.servers = append(j.servers, s)
		j.mu.Unlock()
	}

	seedFile := filepath.Join(share, "seed.bundle")
	if err := j.fetchSeed(ctx, seedFile); err != nil {
		return failed("workspace seed: %v", err)
	}

	pack, err := j.d.cfg.Smolvm.Pack(j.offer.Run.Image)
	if err != nil {
		return failed("%v", err)
	}
	r := j.offer.Job.Requires
	spec := machine.Spec{
		Name: MachineName(j.id(), j.attempt()), Pack: pack, User: j.d.cfg.Smolvm.User,
		CPUs: int(math.Ceil(r.CPU)), MemMiB: int(math.Ceil(r.MemGiB * 1024)), DiskGiB: int(math.Ceil(r.DiskGiB)),
		Labels: map[string]string{"worker-daemon.job": j.id(), "worker-daemon.attempt": strconv.Itoa(j.attempt())},
		Share:  share, Forwards: forwards,
	}
	m, err := j.d.backend.Create(ctx, spec)
	if err != nil {
		return failed("machine: %v", err)
	}
	j.mu.Lock()
	j.m, j.state = m, protocol.MachineRunning
	j.mu.Unlock()
	if ctx.Err() != nil {
		j.stopMachine()
		return failed("%v", context.Cause(ctx))
	}
	j.log.Info("machine running", slog.String("machine", m.Name()))

	seed, err := j.seedWorkspace(ctx, m)
	if err != nil {
		return failed("workspace seed: %v", err)
	}
	_ = os.Remove(seedFile)

	code, runErr := j.runAgent(ctx, m)
	if ctx.Err() != nil {
		return failed("%v", context.Cause(ctx))
	}
	exportNote, exportErr := j.export(ctx, m, share, seed)
	j.reportUsage(ctx, m)
	switch {
	case runErr != nil:
		return failed("agent: %v", runErr)
	case exportErr != nil:
		return failed("export: %v", exportErr)
	case code != 0:
		return failed("agent exited with status %d; %s", code, exportNote)
	}
	return protocol.OutcomeSucceeded, "agent exited with status 0; " + exportNote
}

func (j *job) fetchSeed(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) // #nosec G302 G304 -- the guest reads it; it holds no secret
	if err != nil {
		return err
	}
	err = j.flows.FetchSeed(ctx, j.offer.Run.SeedPath, f)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// guest runs argv in the machine and returns its standard output, or an
// error with its exit status and standard error.
func (j *job) guest(ctx context.Context, m machine.Machine, argv ...string) (string, error) {
	var out, errOut bytes.Buffer
	code, err := m.Exec(ctx, machine.Exec{Argv: argv, Stdout: &out, Stderr: &limited{w: &errOut, n: 4096}})
	if err != nil {
		return "", fmt.Errorf("%s: %w", argv[0], err)
	}
	if code != 0 {
		return "", fmt.Errorf("%s exited with status %d: %s", strings.Join(argv[:min(3, len(argv))], " "), code, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}

// seed is what the workspace was seeded with.
type seed struct {
	Branch string
	// Tips holds the commit of each ref of the seed bundle.
	Tips []string
	Tip  string
}

// parseHeads reads `git bundle list-heads`. The checked-out branch is
// the branch at HEAD's commit; when several are there, as when the
// issue branch has not left its base yet, it is the one named after
// the job, the issue identifier (sortie/ONS-7 for ONS-7). A bundle
// without HEAD must hold one branch.
func parseHeads(out, jobID string) (seed, error) {
	var s seed
	var head string
	branches := map[string]string{}
	var names []string
	for line := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		sha, ref, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		if !slices.Contains(s.Tips, sha) {
			s.Tips = append(s.Tips, sha)
		}
		switch {
		case ref == "HEAD":
			head = sha
		case strings.HasPrefix(ref, "refs/heads/"):
			branches[ref] = sha
			names = append(names, ref)
		}
	}
	slices.Sort(names)
	var at []string
	for _, ref := range names {
		if head == "" || branches[ref] == head {
			at = append(at, ref)
		}
	}
	if len(at) > 1 {
		var named []string
		for _, ref := range at {
			if b := strings.TrimPrefix(ref, "refs/heads/"); b == jobID || strings.HasSuffix(b, "/"+jobID) {
				named = append(named, ref)
			}
		}
		at = named
	}
	if len(at) == 1 {
		s.Branch, s.Tip = strings.TrimPrefix(at[0], "refs/heads/"), branches[at[0]]
		return s, nil
	}
	return seed{}, fmt.Errorf("the seed bundle names no one checked-out branch for %s: %q", jobID, out)
}

func (j *job) seedWorkspace(ctx context.Context, m machine.Machine) (seed, error) {
	bundle := machine.GuestShare + "/seed.bundle"
	out, err := j.guest(ctx, m, "git", "bundle", "list-heads", bundle)
	if err != nil {
		return seed{}, err
	}
	s, err := parseHeads(out, j.id())
	if err != nil {
		return seed{}, err
	}
	ws := machine.GuestWorkspace
	for _, argv := range [][]string{
		{"git", "init", "-q", ws},
		{"git", "-C", ws, "fetch", "-q", "--update-head-ok", bundle, "+refs/*:refs/*"},
		{"git", "-C", ws, "symbolic-ref", "HEAD", "refs/heads/" + s.Branch},
		{"git", "-C", ws, "reset", "-q", "--hard"},
	} {
		if _, err := j.guest(ctx, m, argv...); err != nil {
			return seed{}, err
		}
	}
	j.log.Info("workspace seeded", slog.String("branch", s.Branch))
	return s, nil
}

// agentEnv is the agent's environment: the run's variables, sorted,
// then the forwarders' addresses. It holds no job token.
func (j *job) agentEnv(m machine.Machine) []string {
	run := j.offer.Run
	var env []string
	for _, k := range slices.Sorted(maps.Keys(run.Env)) {
		env = append(env, k+"="+run.Env[k])
	}
	proxy := "http://" + m.GuestAddr(protocol.FlowEgress)
	for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
		env = append(env, k+"="+proxy)
	}
	env = append(env, "NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		MCPURLEnv+"=http://"+m.GuestAddr(protocol.FlowMCP)+"/")
	return env
}

// runAgent runs the agent with its standard input and output on the
// stdio flow, until it exits.
func (j *job) runAgent(ctx context.Context, m machine.Machine) (int, error) {
	run := j.offer.Run
	if run.Command == "" {
		return -1, errors.New("the run names no command")
	}
	conn, err := j.flows.DialStdio(ctx)
	if err != nil {
		return -1, err
	}
	defer func() { _ = conn.Close() }()
	stdin, stdinW := io.Pipe()
	go func() { _ = flows.PumpStdin(conn, stdinW) }()
	var stderr bytes.Buffer
	j.log.Info("agent starting", slog.String("command", run.Command))
	code, err := m.Exec(ctx, machine.Exec{
		Argv: append([]string{run.Command}, run.Args...), Env: j.agentEnv(m), Workdir: machine.GuestWorkspace,
		Stdin: stdin, Stdout: flows.StdioWriter{C: conn}, Stderr: &limited{w: &stderr, n: 64 << 10},
	})
	_ = stdin.Close()
	if err == nil {
		j.log.Info("agent exited", slog.Int("status", code), slog.Int64("egress_requests", j.egress.Requests()))
	}
	if code != 0 && stderr.Len() > 0 {
		j.log.Info("agent standard error", slog.String("tail", tailString(stderr.String(), 2048)))
	}
	return code, err
}

// export returns the commits the run added to the seeded branch as a
// bundle on the bundles flow.
func (j *job) export(ctx context.Context, m machine.Machine, share string, s seed) (string, error) {
	ws := machine.GuestWorkspace
	out, err := j.guest(ctx, m, "git", "-C", ws, "rev-parse", "--verify", "refs/heads/"+s.Branch)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == s.Tip {
		j.log.Info("no new commits to export", slog.String("branch", s.Branch))
		return "no new commits on " + s.Branch, nil
	}
	argv := []string{"git", "-C", ws, "bundle", "create", "-q", machine.GuestShare + "/result.bundle", "refs/heads/" + s.Branch, "--not"}
	if _, err := j.guest(ctx, m, append(argv, s.Tips...)...); err != nil {
		return "", err
	}
	f, err := os.Open(filepath.Join(share, "result.bundle")) // #nosec G304 -- the job's own directory
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if err := j.flows.UploadResult(ctx, f); err != nil {
		return "", err
	}
	j.log.Info("bundle exported", slog.String("branch", s.Branch))
	return "exported the new commits of " + s.Branch, nil
}

// usageAnswer is what a usage command prints.
type usageAnswer struct {
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
}

// readUsage runs the agent kind's usage command and returns the usage
// the daemon reports. A run that made model requests and whose amount
// cannot be read reports spend with the amount unknown, never zero.
func (j *job) readUsage(ctx context.Context, m machine.Machine) protocol.Usage {
	u := protocol.Usage{Job: j.id(), Attempt: j.attempt(), ModelRequests: j.egress.Requests()}
	unknown := func(why string) protocol.Usage {
		if u.ModelRequests == 0 {
			zero := int64(0)
			u.SpendOccurred, u.AmountKnown = false, true
			u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens = &zero, &zero, &zero, &zero
			u.Reason = "no model requests; " + why
			return u
		}
		u.SpendOccurred, u.AmountKnown, u.Reason = true, false, why
		return u
	}
	argv := j.d.cfg.UsageCommand(j.offer.Run.Command)
	if len(argv) == 0 {
		return unknown(fmt.Sprintf("no usage command is configured for %q", filepath.Base(j.offer.Run.Command)))
	}
	var out, errOut bytes.Buffer
	code, err := m.Exec(ctx, machine.Exec{Argv: argv, Env: j.agentEnv(m), Workdir: machine.GuestWorkspace,
		Stdout: &limited{w: &out, n: 1 << 20}, Stderr: &limited{w: &errOut, n: 4096}})
	switch {
	case err != nil:
		return unknown("usage command: " + err.Error())
	case code != 0:
		return unknown(fmt.Sprintf("usage command exited with status %d: %s", code, tailString(strings.TrimSpace(errOut.String()), 512)))
	}
	var a usageAnswer
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &a); err != nil || a.InputTokens == nil || a.OutputTokens == nil {
		return unknown("usage command printed no input_tokens and output_tokens")
	}
	u.AmountKnown = true
	u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens = a.InputTokens, a.OutputTokens, a.CacheReadTokens, a.CacheWriteTokens
	u.SpendOccurred = *a.InputTokens > 0 || *a.OutputTokens > 0 || u.ModelRequests > 0
	return u
}

func (j *job) reportUsage(ctx context.Context, m machine.Machine) {
	u := j.readUsage(ctx, m)
	if err := j.flows.PostUsage(ctx, u); err != nil {
		j.log.Error("usage not delivered", slog.String("error", err.Error()))
		return
	}
	j.log.Info("usage reported", slog.Bool("spend_occurred", u.SpendOccurred), slog.Bool("amount_known", u.AmountKnown), slog.Int64("model_requests", u.ModelRequests))
}

// limited keeps the first n bytes written and drops the rest.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		_, _ = l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}

func tailString(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}
