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
	"sync/atomic"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/cp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/flows"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/stdio"
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
		_, err := j.d.cp.Heartbeat(ctx, j.id(), j.attempt(), j.beat())
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
		Labels: map[string]string{machine.LabelJob: j.id(), "worker-daemon.attempt": strconv.Itoa(j.attempt())},
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
	// Usage goes before the result: once the control plane has the
	// result it finishes the attempt, and a later report could find the
	// attempt's flows closed. The result goes before the report.
	j.reportUsage(ctx, m)
	exportNote, exportErr := j.returnResult(ctx, m, share, seed)
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
		// The agent's control directory, with .sortie/pr.json, is
		// returned beside the bundle, never committed by git add -A.
		{"sh", "-c", `mkdir -p "$1/.git/info" && echo /.sortie >> "$1/.git/info/exclude"`, "sh", ws},
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
		MCPURLEnv+"=http://"+m.GuestAddr(protocol.FlowMCP)+"/",
		StepFileEnv+"="+machine.GuestShare+"/"+StepFile)
	return env
}

// runAgent runs the agent with its standard streams on the stdio flow
// until it exits, and then sends its exit status, the stream's last
// frame. A stream that breaks before the control plane closed the
// agent's input, as when Sortie restarts, does not end the job: the
// daemon stops the agent, keeps the machine, dials the stream again
// until the control plane serves it or the lease ends, and starts the
// agent again in the same workspace, where the restarted control plane
// resumes the agent's session.
func (j *job) runAgent(ctx context.Context, m machine.Machine) (int, error) {
	if j.offer.Run.Command == "" {
		return -1, errors.New("the run names no command")
	}
	conn, err := j.flows.DialStdio(ctx)
	if err != nil {
		return -1, err
	}
	for {
		code, broken, err := j.runAgentOnce(ctx, m, conn)
		if !broken || ctx.Err() != nil {
			return code, err
		}
		j.log.Warn("stdio stream broke before the agent's input closed: keeping the machine and dialing the stream again")
		if conn, err = j.redialStdio(ctx); err != nil {
			return -1, err
		}
		j.log.Info("stdio stream open again; starting the agent again")
	}
}

// redialStdio dials the stdio stream until the control plane serves it.
// It gives up when the job ends, as at a lost lease, or when the control
// plane refuses the job's token: then it no longer knows the job.
func (j *job) redialStdio(ctx context.Context) (*stdio.Conn, error) {
	var conn *stdio.Conn
	err := j.retry(ctx, "stdio stream", func() (bool, error) {
		c, err := j.flows.DialStdio(ctx)
		if err != nil {
			refused, ok := errors.AsType[*stdio.RefusedError](err)
			return !ok || retryable(refused.Status), err
		}
		conn = c
		return false, nil
	})
	return conn, err
}

// retryable reports whether a control plane's answer may change once a
// restarting Sortie is back: an attempt it has not opened yet, a stream
// it still holds, or a proxy in front of it.
func retryable(status int) bool {
	switch status {
	case http.StatusNotFound, http.StatusConflict, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryableFlow reports whether a flow request may succeed once a
// restarting control plane is back: it never reached it, or it was
// answered as [retryable] says.
func retryableFlow(err error) bool {
	if se, ok := errors.AsType[*flows.StatusError](err); ok {
		return retryable(se.Status) && se.Status != http.StatusConflict
	}
	return err != nil
}

// retry calls try until it succeeds, reports its failure final, or the
// job ends, waiting longer between tries up to a heartbeat interval.
func (j *job) retry(ctx context.Context, what string, try func() (again bool, err error)) error {
	wait := 100 * time.Millisecond
	for {
		again, err := try()
		if err == nil || !again {
			return err
		}
		j.log.Debug("retrying", slog.String("what", what), slog.String("error", err.Error()), slog.Duration("in", wait))
		select {
		case <-ctx.Done():
			return fmt.Errorf("%s: %w (%w)", what, context.Cause(ctx), err)
		case <-time.After(wait):
		}
		wait = min(2*wait, j.d.heartbeatInterval())
	}
}

// runAgentOnce runs the agent over conn, and reports whether the stream
// broke before the control plane closed the agent's input.
func (j *job) runAgentOnce(ctx context.Context, m machine.Machine, conn *stdio.Conn) (int, bool, error) {
	run := j.offer.Run
	defer func() { _ = conn.Close() }()
	agentCtx, stopAgent := context.WithCancel(ctx)
	defer stopAgent()
	var broken atomic.Bool
	stdin, stdinW := io.Pipe()
	go func() {
		closed, err := pumpStdin(conn, stdinW)
		if !closed {
			broken.Store(true)
			stopAgent()
		}
		if err != nil {
			stdinW.CloseWithError(err)
			if ctx.Err() == nil {
				j.log.Warn("stdio input ended", slog.String("error", err.Error()))
			}
			return
		}
		_ = stdinW.Close()
	}()
	var stderr bytes.Buffer
	j.log.Info("agent starting", slog.String("command", run.Command))
	code, err := m.Exec(agentCtx, machine.Exec{
		Argv: append([]string{run.Command}, run.Args...), Env: j.agentEnv(m), Workdir: machine.GuestWorkspace,
		Stdin:  stdin,
		Stdout: stdio.Writer{C: conn, Type: stdio.Stdout},
		Stderr: io.MultiWriter(&limited{w: &stderr, n: 64 << 10}, lenient{stdio.Writer{C: conn, Type: stdio.Stderr}}),
	})
	_ = stdin.Close()
	if broken.Load() {
		return -1, true, nil
	}
	if err == nil {
		j.log.Info("agent exited", slog.Int("status", code), slog.Int64("egress_requests", j.egress.Requests()))
	}
	if code != 0 && stderr.Len() > 0 {
		j.log.Info("agent standard error", slog.String("tail", tailString(stderr.String(), 2048)))
	}
	if ctx.Err() == nil {
		if xerr := conn.WriteFrame(stdio.Exit, stdio.ExitPayload(code)); xerr != nil {
			j.log.Warn("the agent's exit did not reach the stdio flow", slog.String("error", xerr.Error()))
		}
	}
	return code, false, err
}

// pumpStdin feeds the agent's standard input from the stream's I frames
// until C, the stream's end, or a frame the control plane does not send,
// and reports whether the input closed: C, or an agent that stopped
// reading. A stream that ends without C is broken; the caller closes w.
func pumpStdin(c *stdio.Conn, w *io.PipeWriter) (bool, error) {
	for {
		typ, payload, err := c.ReadFrame()
		switch {
		case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed):
			return false, nil
		case err != nil:
			return false, err
		case typ == stdio.StdinClosed:
			return true, nil
		case typ != stdio.Stdin:
			return true, fmt.Errorf("unexpected stdio frame type %q from the control plane", typ)
		}
		if _, err := w.Write(payload); err != nil {
			return true, nil
		}
	}
}

// lenient drops what its writer refuses, so that the loss of standard
// error on the stream does not stop the agent's diagnostics in the log.
type lenient struct{ w io.Writer }

func (l lenient) Write(p []byte) (int, error) {
	_, _ = l.w.Write(p)
	return len(p), nil
}

// guestPRFile is the agent's pull request suggestion, returned beside
// the bundle.
const guestPRFile = machine.GuestWorkspace + "/.sortie/pr.json"

// returnResult returns the issue branch on the bundles flow: a bundle of
// refs/heads/<branch> that leaves out the seed's commits (a branch with
// nothing new is bundled as its tip alone), and the agent's
// .sortie/pr.json beside it when there is one. The bundle part is left
// out when the machine has no issue branch.
func (j *job) returnResult(ctx context.Context, m machine.Machine, share string, s seed) (string, error) {
	ws := machine.GuestWorkspace
	var res flows.Result
	bundle := ""
	note := ""
	var out bytes.Buffer
	code, err := m.Exec(ctx, machine.Exec{Argv: []string{"git", "-C", ws, "rev-parse", "-q", "--verify", "refs/heads/" + s.Branch + "^{commit}"},
		Stdout: &out, Stderr: io.Discard})
	if err != nil {
		return "", fmt.Errorf("git: %w", err)
	}
	tip := strings.TrimSpace(out.String())
	switch {
	case code != 0 || tip == "":
		j.log.Info("the machine has no issue branch; returning no bundle", slog.String("branch", s.Branch))
		note = "no issue branch " + s.Branch + " in the machine"
	default:
		exclude := s.Tips
		note = "returned the new commits of " + s.Branch
		if tip == s.Tip {
			parents, err := j.guest(ctx, m, "git", "-C", ws, "rev-parse", tip+"^@")
			if err != nil {
				return "", err
			}
			exclude = strings.Fields(parents)
			note = "no new commits on " + s.Branch + "; returned its tip"
		}
		argv := []string{"git", "-C", ws, "bundle", "create", "-q", machine.GuestShare + "/result.bundle", "refs/heads/" + s.Branch}
		if len(exclude) > 0 {
			argv = append(append(argv, "--not"), exclude...)
		}
		if _, err := j.guest(ctx, m, argv...); err != nil {
			return "", err
		}
		bundle = filepath.Join(share, "result.bundle")
	}
	pr, err := j.readPR(ctx, m)
	if err != nil {
		return "", err
	}
	res.PR = pr
	// A control plane that restarts takes the result once it is back.
	if err := j.retry(ctx, "result", func() (bool, error) {
		res.Bundle = nil
		if bundle != "" {
			f, err := os.Open(bundle) // #nosec G304 -- the job's own directory
			if err != nil {
				return false, err
			}
			defer func() { _ = f.Close() }()
			res.Bundle = f
		}
		err := j.flows.UploadResult(ctx, res)
		return retryableFlow(err), err
	}); err != nil {
		return "", err
	}
	j.log.Info("result returned", slog.String("branch", s.Branch), slog.Bool("bundle", bundle != ""), slog.Bool("pr", pr != nil))
	if pr != nil {
		note += ", with .sortie/pr.json"
	}
	return note, nil
}

// readPR returns the agent's .sortie/pr.json, or nil when it wrote
// none. One over 64 KiB fails the return, as the control plane would
// refuse it.
func (j *job) readPR(ctx context.Context, m machine.Machine) ([]byte, error) {
	var out bytes.Buffer
	script := `test -f "$1" || exit 3; exec cat "$1"`
	code, err := m.Exec(ctx, machine.Exec{Argv: []string{"sh", "-c", script, "sh", guestPRFile},
		Stdout: &limited{w: &out, n: protocol.MaxPRFile + 1}, Stderr: io.Discard})
	switch {
	case err != nil:
		return nil, fmt.Errorf("reading .sortie/pr.json: %w", err)
	case code == 3:
		return nil, nil
	case code != 0:
		return nil, fmt.Errorf("reading .sortie/pr.json: exit status %d", code)
	case out.Len() > protocol.MaxPRFile:
		return nil, errors.New(".sortie/pr.json exceeds 64 KiB")
	}
	return out.Bytes(), nil
}

// usageAnswer is what a usage command prints.
type usageAnswer struct {
	InputTokens      *int64 `json:"input_tokens"`
	OutputTokens     *int64 `json:"output_tokens"`
	CacheReadTokens  *int64 `json:"cache_read_tokens"`
	CacheWriteTokens *int64 `json:"cache_write_tokens"`
	Model            string `json:"model"`
}

// readUsage runs the agent kind's usage command and returns the usage
// the daemon reports and why it is what it is. A run that made model
// requests and whose amount cannot be read reports "measured": false:
// spend occurred, amount unknown, never zero. A run that made no model
// request and whose amount cannot be read spent nothing: zero, measured.
func (j *job) readUsage(ctx context.Context, m machine.Machine) (protocol.Usage, string) {
	requests := j.egress.Requests()
	unknown := func(why string) (protocol.Usage, string) {
		if requests == 0 {
			return protocol.Usage{Measured: true}, "no model requests; " + why
		}
		return protocol.Usage{Measured: false}, why
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
	u := protocol.Usage{InputTokens: *a.InputTokens, OutputTokens: *a.OutputTokens, Model: a.Model, Measured: true}
	if a.CacheReadTokens != nil {
		u.CacheReadTokens = *a.CacheReadTokens
	}
	if a.CacheWriteTokens != nil {
		u.CacheWriteTokens = *a.CacheWriteTokens
	}
	for _, n := range []int64{u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens} {
		if n < 0 {
			return unknown("usage command printed a negative count")
		}
	}
	return u, "read by the usage command"
}

func (j *job) reportUsage(ctx context.Context, m machine.Machine) {
	u, why := j.readUsage(ctx, m)
	if err := j.retry(ctx, "usage", func() (bool, error) {
		err := j.flows.PostUsage(ctx, u)
		return retryableFlow(err), err
	}); err != nil {
		j.log.Error("usage not delivered", slog.String("error", err.Error()))
		return
	}
	j.log.Info("usage reported", slog.Bool("measured", u.Measured), slog.Int64("input_tokens", u.InputTokens),
		slog.Int64("output_tokens", u.OutputTokens), slog.Int64("model_requests", j.egress.Requests()), slog.String("why", why))
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
