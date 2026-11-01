package daemon_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/config"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

func indexOf(events []fakecp.Event, from int, match func(fakecp.Event) bool) int {
	for i := from; i < len(events); i++ {
		if match(events[i]) {
			return i
		}
	}
	return -1
}

//nolint:unparam // every scenario names ONS-7, as the spec does
func is(kind, job string, attempt int) func(fakecp.Event) bool {
	return func(e fakecp.Event) bool {
		return e.Kind == kind && (job == "" || (e.Job == job && e.Attempt == attempt))
	}
}

// Scenario: Against a fake control plane. The daemon starts against a
// fake control plane that offers one job; the fake sees a check-in, a
// poll, heartbeats while the job runs, and one report.
func TestAgainstAFakeControlPlane(t *testing.T) {
	h := newHarness(t, defaults())
	// Heartbeats come every 200 ms; the agent works for a second.
	h.offer("ONS-7", 1, map[string]string{"AGENT_DELAY": "1s"})
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	// Another heartbeat interval or two: no second report comes.
	time.Sleep(500 * time.Millisecond)

	ev := h.fake.Events()
	checkIn := indexOf(ev, 0, func(e fakecp.Event) bool { return e.Kind == "checkin" && e.Status == 200 })
	if checkIn != 0 {
		t.Fatalf("the first request is not a check-in: %s", h.describe())
	}
	poll := indexOf(ev, checkIn, func(e fakecp.Event) bool { return e.Kind == "poll" && e.Job == "ONS-7" })
	report := indexOf(ev, 0, is("report", "ONS-7", 1))
	if poll < 0 || report < poll {
		t.Fatalf("want a poll that took ONS-7 before its report: %s", h.describe())
	}
	beats := 0
	for _, e := range ev[poll:report] {
		if e.Kind == "heartbeat" && e.Job == "ONS-7" && e.Status == 200 {
			beats++
		}
	}
	if beats < 2 {
		t.Errorf("want heartbeats while the job ran, got %d: %s", beats, h.describe())
	}
	if n := h.fake.Count("report", "ONS-7", 1); n != 1 {
		t.Errorf("want one report, got %d", n)
	}
	if n := h.fake.Count("checkin", "", 0); n < 3 {
		t.Errorf("want a check-in at start and every heartbeat_ms, got %d", n)
	}
	if n := h.fake.Count("bad-protocol", "", 0); n != 0 {
		t.Errorf("%d requests lacked %s: 1", n, protocol.ProtocolHeader)
	}
	r := h.reports("ONS-7", 1)[0]
	if r.Outcome != protocol.OutcomeSucceeded || r.Machine != protocol.MachineDeleted {
		t.Errorf("report = %+v, want succeeded with the machine deleted", r)
	}
}

// Scenario: One job end to end. The fake receives the agent's stdio on
// stdio, its model traffic on egress, its tool calls on mcp, and a
// bundle on bundles, before the report.
func TestOneJobEndToEnd(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, nil)
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })

	ev := h.fake.Events()
	report := indexOf(ev, 0, is("report", "ONS-7", 1))
	for _, kind := range []string{"seed", "stdio", "stdio-out", "egress", "mcp", "bundle", "usage"} {
		if i := indexOf(ev, 0, is(kind, "ONS-7", 1)); i < 0 || i > report {
			t.Errorf("no %s event for ONS-7 before the report: %s", kind, h.describe())
		}
	}
	out := h.fake.StdioOutput("ONS-7", 1)
	if !strings.Contains(out, "agent got: hello agent\n") || !strings.Contains(out, "done\n") {
		t.Errorf("stdio output = %q, want the agent's answer to the prompt", out)
	}
	var sawModel bool
	for _, e := range ev {
		if e.Kind == "egress" && e.Job == "ONS-7" && bytes.HasPrefix(e.Body, []byte("POST model.example:443/v1/messages")) {
			sawModel = true
		}
	}
	if !sawModel {
		t.Errorf("the model request did not reach the egress flow: %s", h.describe())
	}
	r := h.reports("ONS-7", 1)
	if len(r) != 1 || r[0].Outcome != protocol.OutcomeSucceeded {
		t.Fatalf("reports = %+v, want one success", r)
	}

	// The bundle holds the agent's commit on the issue branch, on top of
	// the seed.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "result.bundle"), h.fake.Result("ONS-7", 1), 0o600); err != nil {
		t.Fatal(err)
	}
	heads, err := exec.Command("git", "bundle", "list-heads", filepath.Join(dir, "result.bundle")).CombinedOutput()
	if err != nil {
		t.Fatalf("list-heads: %v: %s", err, heads)
	}
	if lines := strings.Split(strings.TrimSpace(string(heads)), "\n"); len(lines) != 1 || !strings.HasSuffix(lines[0], " refs/heads/sortie/ONS-7") {
		t.Errorf("bundle heads = %q, want only refs/heads/sortie/ONS-7", heads)
	}
	ws := h.machine("ONS-7", 1).HostPath("/workspace")
	log, err := exec.Command("git", "-C", ws, "log", "--format=%s", "sortie/ONS-7").Output()
	if err != nil || string(log) != "agent work\nseed\n" {
		t.Errorf("workspace log = %q, %v; want the agent's commit on the seed", log, err)
	}
	if !h.machine("ONS-7", 1).Deleted() {
		t.Error("the machine was not deleted after the report")
	}
}

// Scenario: Memory below the reserve. With a 16 GiB reserve and 20 GiB
// available, the daemon declares free.mem_gib as 4.
func TestMemoryBelowTheReserve(t *testing.T) {
	s := defaults()
	s.reserveMem = 16
	s.host.MemAvailableGiB = 20
	h := newHarness(t, s)
	h.waitFor("a check-in", 5*time.Second, func() bool { _, ok := h.fake.Declaration("taichi"); return ok })
	d, _ := h.fake.Declaration("taichi")
	if d.Free.MemGiB != 4 {
		t.Errorf("free.mem_gib = %g, want 4", d.Free.MemGiB)
	}
	if d.Free.DiskGiB != 190 {
		t.Errorf("free.disk_gib = %g, want 200 less the 10 GiB floor", d.Free.DiskGiB)
	}
	if d.RunSlots != 1 || d.Running != 0 || d.OS != "linux" || len(d.Backends) != 1 || d.Backends[0] != "smolvm" {
		t.Errorf("declaration = %+v", d)
	}
}

func TestFreeNeverNegative(t *testing.T) {
	s := defaults()
	s.host.MemAvailableGiB = 10
	s.host.DiskAvailableGiB = 5
	s.noStart = true
	h := newHarness(t, s)
	f := h.d.Free(s.host)
	if f.MemGiB != 0 || f.DiskGiB != 0 {
		t.Errorf("free = %+v, want 0 below the reserve and the floor", f)
	}
}

// The daemon does not poll while what it would declare is below the
// smallest configured job.
func TestNoPollBelowTheSmallestJob(t *testing.T) {
	s := defaults()
	s.host.MemAvailableGiB = 20 // 4 GiB above the reserve
	s.smallest = config.Size{MemGiB: 8}
	h := newHarness(t, s)
	h.offer("ONS-7", 1, nil)
	h.waitFor("three check-ins", 5*time.Second, func() bool { return h.fake.Count("checkin", "", 0) >= 3 })
	if n := h.fake.Count("poll", "", 0); n != 0 {
		t.Errorf("polled %d times with 4 GiB free and an 8 GiB smallest job", n)
	}

	// Once the host frees memory, it polls and takes the job.
	h.probe.mu.Lock()
	h.probe.s.MemAvailableGiB = 40
	h.probe.mu.Unlock()
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
}

// The daemon does not start a job that would take the host below its
// reserve or floor; it reports the attempt failed, with no machine.
func TestNoStartBelowTheReserve(t *testing.T) {
	for name, tc := range map[string]struct {
		mem, disk float64
		want      string
	}{
		"memory": {mem: 19, disk: 200, want: "GiB memory"}, // 3 GiB above the reserve
		"disk":   {mem: 40, disk: 15, want: "GiB disk"},
	} {
		t.Run(name, func(t *testing.T) {
			s := defaults()
			s.host.MemAvailableGiB, s.host.DiskAvailableGiB = tc.mem, tc.disk
			s.smallest = config.Size{MemGiB: 1, DiskGiB: 1}
			h := newHarness(t, s)
			h.offer("ONS-7", 1, nil) // needs 4 GiB memory and 10 GiB disk
			h.waitFor("the report", 10*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
			r := h.reports("ONS-7", 1)[0]
			if r.Outcome != protocol.OutcomeFailed || r.Machine != protocol.MachineDeleted || !strings.Contains(r.Reason, tc.want) {
				t.Errorf("report = %+v, want a failure naming %q", r, tc.want)
			}
			if len(h.backend.Machines()) != 0 {
				t.Error("a machine was created")
			}
		})
	}
}

// Scenario: The guest sees no job token. No job token appears in the
// machine's environment, files or command line; and none in the
// daemon's log or on its disk.
func TestTheGuestSeesNoJobToken(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_DELAY": "300ms"})
	var tokens map[string]string
	h.waitFor("the agent's answer", 20*time.Second, func() bool {
		if tokens == nil {
			if t := h.fake.Tokens("ONS-7", 1); len(t) == len(protocol.Flows) {
				tokens = t
			}
		}
		return tokens != nil && h.fake.Count("stdio-out", "ONS-7", 1) > 0
	})
	// While the job runs: the daemon's state directory, sockets aside.
	stateFiles := readTree(t, h.cfg.StateDir)
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })

	m := h.machine("ONS-7", 1)
	var guest strings.Builder
	for _, e := range m.Execs() {
		guest.WriteString(strings.Join(e.Argv, " ") + "\n" + strings.Join(e.Env, "\n") + "\n" + e.Workdir + "\n")
	}
	spec := m.Spec()
	specJSON, _ := json.Marshal(spec)
	guestFiles := readTree(t, m.Root())
	if !strings.Contains(guestFiles, "SORTIE_ISSUE_IDENTIFIER=ONS-7") {
		t.Fatal("the agent's own environment was not captured")
	}
	for flow, tok := range tokens {
		for where, text := range map[string]string{
			"the guest's command lines and environment": guest.String(),
			"the machine's spec":                        string(specJSON),
			"the guest's files":                         guestFiles,
			"the daemon's state directory":              stateFiles,
			"the daemon's log":                          h.logs.String(),
		} {
			if strings.Contains(text, tok) {
				t.Errorf("the %s token appears in %s", flow, where)
			}
		}
	}
}

// readTree returns the contents of every regular file under dir.
func readTree(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(p)
		if err == nil {
			b.WriteString(p + "\n")
			b.Write(data)
		}
		return nil
	})
	return b.String()
}

// Scenario: The control plane ended the attempt. A heartbeat for
// ONS-7, attempt 1, answers 409: the daemon stops ONS-7's machine
// within one heartbeat interval and sends no report for attempt 1.
func TestALostLeaseStopsTheJob(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_MODE": "sleep"})
	h.waitFor("the agent to run", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "sleeping")
	})
	m := h.machine("ONS-7", 1)
	if !m.StoppedAt().IsZero() {
		t.Fatal("the machine stopped before the lease was lost")
	}
	h.fake.AnswerHeartbeat("ONS-7", 1, 409)
	var lost time.Time
	h.waitFor("a 409 heartbeat", 5*time.Second, func() bool {
		for _, e := range h.fake.Events() {
			if e.Kind == "heartbeat" && e.Job == "ONS-7" && e.Status == 409 {
				lost = e.At
				return true
			}
		}
		return false
	})
	heartbeat := time.Duration(h.fake.LeaseMS/3) * time.Millisecond
	h.waitFor("the machine to stop", 5*time.Second, func() bool { return !m.StoppedAt().IsZero() })
	if took := m.StoppedAt().Sub(lost); took > heartbeat {
		t.Errorf("the machine stopped %v after the 409, more than one heartbeat interval (%v)", took, heartbeat)
	}
	h.waitFor("the job to end", 10*time.Second, func() bool { return len(h.d.Listeners()) == 0 })
	time.Sleep(3 * heartbeat)
	if n := h.fake.Count("report", "ONS-7", 1); n != 0 {
		t.Errorf("sent %d reports for attempt 1 after losing its lease", n)
	}
	if n := h.fake.Count("bundle", "ONS-7", 1) + h.fake.Count("usage", "ONS-7", 1); n != 0 {
		t.Errorf("sent %d bundle or usage requests after losing the lease", n)
	}
}

func usageOf(t *testing.T, h *harness) protocol.Usage {
	t.Helper()
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	u, ok := h.fake.Usage("ONS-7", 1)
	if !ok {
		t.Fatalf("no usage reported: %s", h.describe())
	}
	return u
}

// Scenario: Usage unreadable. The usage command fails after a run that
// made model requests: the daemon reports spend occurred with the
// amount unknown.
func TestUsageUnreadable(t *testing.T) {
	for _, mode := range []string{"fail", "garbled"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, defaults())
			h.offer("ONS-7", 1, map[string]string{"USAGE_MODE": mode})
			u := usageOf(t, h)
			if !u.SpendOccurred || u.AmountKnown || u.ModelRequests < 1 {
				t.Errorf("usage = %+v, want spend occurred, amount unknown", u)
			}
			if u.InputTokens != nil || u.OutputTokens != nil {
				t.Errorf("usage carries token amounts %v %v; want none, never zero", u.InputTokens, u.OutputTokens)
			}
		})
	}
	t.Run("no usage command", func(t *testing.T) {
		s := defaults()
		s.noUsage = true
		h := newHarness(t, s)
		h.offer("ONS-7", 1, nil)
		if u := usageOf(t, h); !u.SpendOccurred || u.AmountKnown {
			t.Errorf("usage = %+v, want spend occurred, amount unknown", u)
		}
	})
}

func TestUsageReadable(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, nil)
	u := usageOf(t, h)
	if !u.AmountKnown || !u.SpendOccurred || u.InputTokens == nil || *u.InputTokens != 1200 || *u.OutputTokens != 300 ||
		u.CacheReadTokens == nil || *u.CacheReadTokens != 50 || u.Job != "ONS-7" || u.Attempt != 1 {
		t.Errorf("usage = %+v, want 1200 in, 300 out, 50 cache read", u)
	}
}

func TestShutdownReportsRunningJobs(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_MODE": "sleep"})
	h.waitFor("the agent to run", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "sleeping")
	})
	h.stop()
	r := h.reports("ONS-7", 1)
	if len(r) != 1 || r[0].Outcome != protocol.OutcomeFailed || r[0].Machine != protocol.MachineDeleted {
		t.Errorf("reports = %+v, want one failure with the machine deleted", r)
	}
}

func TestDeclarationWithoutBackend(t *testing.T) {
	s := defaults()
	s.noStart = true
	h := newHarness(t, s)
	d := h.d.Declaration(hostinfo.Snapshot{OS: "darwin", Arch: "arm64", CPUs: 10, MemAvailableGiB: 30, DiskAvailableGiB: 100})
	if d.Backends == nil || len(d.Backends) != 0 {
		t.Errorf("backends = %v before the backend was checked, want []", d.Backends)
	}
	if d.Free.CPU != 10 || d.Free.MemGiB != 14 || d.Free.DiskGiB != 90 {
		t.Errorf("free = %+v", d.Free)
	}
}
