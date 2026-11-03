package daemon_test

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
// stdio, its model traffic on egress, its tool calls on mcp, and the
// result on bundles, before the report.
//
// Scenario: One attempt over the wire, against the fake: the seed, the
// agent's stdio in frames ending with X, usage, then the result with
// pr, each with its own token, and the report last.
func TestOneJobEndToEnd(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, nil)
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })

	ev := h.fake.Events()
	report := indexOf(ev, 0, is("report", "ONS-7", 1))
	for _, kind := range []string{"seed", "stdio", "stdio-O", "stdio-E", "stdio-X", "egress", "mcp", "usage", "result"} {
		if i := indexOf(ev, 0, is(kind, "ONS-7", 1)); i < 0 || i > report {
			t.Errorf("no %s event for ONS-7 before the report: %s", kind, h.describe())
		}
	}
	// X is the stream's last frame, and the result comes after it.
	exit := indexOf(ev, 0, is("stdio-X", "ONS-7", 1))
	for i, e := range ev {
		if e.Job == "ONS-7" && (e.Kind == "stdio-O" || e.Kind == "stdio-E") && i > exit {
			t.Errorf("a %s frame came after X: %s", e.Kind, h.describe())
		}
	}
	if r := indexOf(ev, 0, is("result", "ONS-7", 1)); r < exit {
		t.Errorf("the result came before the agent's exit: %s", h.describe())
	}
	if code, ok := h.fake.Exit("ONS-7", 1); !ok || code != 0 || h.fake.StreamBroken("ONS-7", 1) {
		t.Errorf("exit = %d, %v; broken = %v; want X with status 0", code, ok, h.fake.StreamBroken("ONS-7", 1))
	}
	for _, e := range ev {
		if e.Kind == "bad-protocol" || ((e.Kind == "seed" || e.Kind == "usage" || e.Kind == "result" || e.Kind == "stdio" || e.Kind == "bundles") && e.Status == 401) {
			t.Errorf("a flow request was refused: %s %d", e.Kind, e.Status)
		}
	}
	out := h.fake.StdioOutput("ONS-7", 1)
	if !strings.Contains(out, "agent got: hello agent\n") || !strings.Contains(out, "done\n") {
		t.Errorf("stdio output = %q, want the agent's answer to the prompt", out)
	}
	if e := h.fake.StdioError("ONS-7", 1); !strings.Contains(e, "agent: working") {
		t.Errorf("stdio error = %q, want the agent's standard error in E frames", e)
	}
	res, ok := h.fake.Result("ONS-7", 1)
	if !ok || !strings.HasPrefix(res.ContentType, "multipart/form-data") {
		t.Fatalf("result = %+v, %v; want a multipart result", res, ok)
	}
	if string(res.PR) != testPR {
		t.Errorf("pr part = %q, want the agent's .sortie/pr.json", res.PR)
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
	if err := os.WriteFile(filepath.Join(dir, "result.bundle"), res.Bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	heads, err := exec.Command("git", "bundle", "list-heads", filepath.Join(dir, "result.bundle")).CombinedOutput()
	if err != nil {
		t.Fatalf("list-heads: %v: %s", err, heads)
	}
	if lines := strings.Split(strings.TrimSpace(string(heads)), "\n"); len(lines) != 1 || !strings.HasSuffix(lines[0], " refs/heads/sortie/ONS-7") {
		t.Errorf("bundle heads = %q, want only refs/heads/sortie/ONS-7", heads)
	}
	if prereqs := bundlePrerequisites(res.Bundle); len(prereqs) == 0 {
		t.Error("the bundle carries the seed's commits; want them left out")
	}
	ws := h.machine("ONS-7", 1).HostPath("/workspace")
	log, err := exec.Command("git", "-C", ws, "log", "--format=%s", "sortie/ONS-7").Output()
	if err != nil || string(log) != "agent work\nseed\n" {
		t.Errorf("workspace log = %q, %v; want the agent's commit on the seed", log, err)
	}
	files, err := exec.Command("git", "-C", ws, "show", "--name-only", "--format=", "sortie/ONS-7").Output()
	if err != nil || !strings.Contains(string(files), "work.txt") || strings.Contains(string(files), ".sortie") {
		t.Errorf("the agent's commit holds %q, %v; want work.txt, never .sortie", files, err)
	}
	if !h.machine("ONS-7", 1).Deleted() {
		t.Error("the machine was not deleted after the report")
	}
}

// bundlePrerequisites returns the commits a bundle needs and leaves
// out: the header lines that start with "-".
func bundlePrerequisites(b []byte) []string {
	head, _, _ := bytes.Cut(b, []byte("\n\n"))
	var out []string
	for line := range strings.SplitSeq(string(head), "\n") {
		if sha, ok := strings.CutPrefix(line, "-"); ok {
			out = append(out, strings.Fields(sha)[0])
		}
	}
	return out
}

func bundleHeads(t *testing.T, b []byte) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "b.bundle")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("git", "bundle", "list-heads", f).CombinedOutput()
	if err != nil {
		t.Fatalf("list-heads: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func resultOf(t *testing.T, h *harness) fakecp.Result {
	t.Helper()
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	ev := h.fake.Events()
	if r, rep := indexOf(ev, 0, is("result", "ONS-7", 1)), indexOf(ev, 0, is("report", "ONS-7", 1)); r < 0 || r > rep {
		t.Fatalf("want the result before the report: %s", h.describe())
	}
	res, ok := h.fake.Result("ONS-7", 1)
	if !ok {
		t.Fatalf("no result: %s", h.describe())
	}
	return res
}

// A run that adds no commit returns the issue branch's tip alone, the
// smallest bundle git makes of it.
func TestResultWithoutNewCommits(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_GIT": "none"})
	res := resultOf(t, h)
	ws := h.machine("ONS-7", 1).HostPath("/workspace")
	tip, err := exec.Command("git", "-C", ws, "rev-parse", "sortie/ONS-7").Output()
	if err != nil {
		t.Fatal(err)
	}
	if heads := bundleHeads(t, res.Bundle); heads != strings.TrimSpace(string(tip))+" refs/heads/sortie/ONS-7" {
		t.Errorf("bundle heads = %q, want the seed's tip of refs/heads/sortie/ONS-7", heads)
	}
	if string(res.PR) != testPR {
		t.Errorf("pr part = %q", res.PR)
	}
	if r := h.reports("ONS-7", 1); len(r) != 1 || r[0].Outcome != protocol.OutcomeSucceeded {
		t.Errorf("reports = %+v, want one success", r)
	}
}

// The bundle part is left out when the machine has no issue branch; the
// result is still returned, with pr.
func TestResultWithoutIssueBranch(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_GIT": "no-branch"})
	res := resultOf(t, h)
	if res.Bundle != nil {
		t.Errorf("the result carries a bundle of %d bytes; want the part left out", len(res.Bundle))
	}
	if string(res.PR) != testPR {
		t.Errorf("pr part = %q", res.PR)
	}
}

// The X frame carries the agent's exit status, a failure too, and the
// result is still returned before the failure report.
func TestExitStatusOnTheStream(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_EXIT": "9"})
	resultOf(t, h)
	if code, ok := h.fake.Exit("ONS-7", 1); !ok || code != 9 {
		t.Errorf("exit = %d, %v; want X with status 9", code, ok)
	}
	if r := h.reports("ONS-7", 1); len(r) != 1 || r[0].Outcome != protocol.OutcomeFailed || !strings.Contains(r[0].Reason, "status 9") {
		t.Errorf("reports = %+v, want one failure naming status 9", r)
	}
}

// I frames are the agent's standard input and C closes it: an agent that
// copies its input until the end exits once C comes.
func TestInputClosedEndsTheInput(t *testing.T) {
	h := newHarness(t, defaults())
	self, _ := os.Executable()
	h.fake.Offer(fakecp.Job{
		ID: "ONS-7", Attempt: 1,
		Requires: protocol.Requires{CPU: 1, MemGiB: 1, DiskGiB: 1},
		Run:      protocol.Run{Backend: "smolvm", Image: testImage, Command: self, Args: []string{"fake-echo"}, Env: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null"}},
		Seed:     seedBundle(t, "sortie/ONS-7"), StdioInput: []string{"one\n", "two\n", strings.Repeat("x", 3<<20) + "\n"},
	})
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	want := "one\ntwo\n" + strings.Repeat("x", 3<<20) + "\n"
	if out := h.fake.StdioOutput("ONS-7", 1); out != want {
		t.Errorf("stdio output is %d bytes, want the %d bytes of input back", len(out), len(want))
	}
	if code, ok := h.fake.Exit("ONS-7", 1); !ok || code != 0 {
		t.Errorf("exit = %d, %v; want X with status 0", code, ok)
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

// Scenario: the slots follow the host. Without run_slots the daemon
// declares the slots the host's CPUs and memory hold, from the snapshot
// of each check-in, so a host that loses memory declares fewer at its
// next check-in; free memory is still the host's MemAvailable.
func TestSlotsFollowTheHost(t *testing.T) {
	s := defaults()
	s.autoSlots = true
	s.reserveMem = 8
	s.smallest = config.Size{CPU: 4, MemGiB: 8, DiskGiB: 20}
	s.host.CPUs, s.host.MemTotalGiB, s.host.MemAvailableGiB = 32, 64, 30
	h := newHarness(t, s)
	h.waitFor("a check-in", 5*time.Second, func() bool { _, ok := h.fake.Declaration("taichi"); return ok })
	d, _ := h.fake.Declaration("taichi")
	if d.RunSlots != 7 || d.Free.MemGiB != 22 {
		t.Errorf("run_slots %d, free.mem_gib %g; want 7 slots and MemAvailable less the reserve, 22", d.RunSlots, d.Free.MemGiB)
	}
	less := s.host
	less.MemTotalGiB = 40
	if got := h.d.Declaration(less).RunSlots; got != 4 {
		t.Errorf("with 40 GiB the daemon declares %d slots, want 4", got)
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
		return tokens != nil && h.fake.Count("stdio-O", "ONS-7", 1) > 0
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
	if n := h.fake.Count("result", "ONS-7", 1) + h.fake.Count("usage", "ONS-7", 1); n != 0 {
		t.Errorf("sent %d result or usage requests after losing the lease", n)
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

// usageBody returns the JSON object the worker posted on usage.
func usageBody(t *testing.T, h *harness) map[string]any {
	t.Helper()
	for _, e := range h.fake.Events() {
		if e.Kind == "usage" && e.Job == "ONS-7" && e.Status == 204 {
			var m map[string]any
			if err := json.Unmarshal(e.Body, &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatalf("no usage reported: %s", h.describe())
	return nil
}

// The usage body has exactly the fields the usage flow takes.
func TestUsageBodyFields(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, nil)
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	var keys []string
	for k := range usageBody(t, h) {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	want := []string{"cache_read_tokens", "cache_write_tokens", "input_tokens", "measured", "model", "output_tokens"}
	if !slices.Equal(keys, want) {
		t.Errorf("usage fields = %v, want %v", keys, want)
	}
}

// Scenario: Unknown amount. A run made model requests and its usage
// cannot be read: the worker posts "measured": false.
func TestUsageUnreadable(t *testing.T) {
	for _, mode := range []string{"fail", "garbled"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, defaults())
			h.offer("ONS-7", 1, map[string]string{"USAGE_MODE": mode})
			if u := usageOf(t, h); u.Measured {
				t.Errorf("usage = %+v, want measured false: spend occurred, amount unknown", u)
			}
			if m := usageBody(t, h); m["measured"] != false {
				t.Errorf("usage body = %v, want \"measured\": false", m)
			}
		})
	}
	t.Run("no usage command", func(t *testing.T) {
		s := defaults()
		s.noUsage = true
		h := newHarness(t, s)
		h.offer("ONS-7", 1, nil)
		if u := usageOf(t, h); u.Measured {
			t.Errorf("usage = %+v, want measured false", u)
		}
	})
}

// A run that made no model request and whose usage cannot be read spent
// nothing: zero, measured.
func TestUsageNoModelRequests(t *testing.T) {
	s := defaults()
	s.noUsage = true
	h := newHarness(t, s)
	self, _ := os.Executable()
	h.fake.Offer(fakecp.Job{
		ID: "ONS-7", Attempt: 1,
		Requires: protocol.Requires{CPU: 1, MemGiB: 1, DiskGiB: 1},
		Run:      protocol.Run{Backend: "smolvm", Image: testImage, Command: self, Args: []string{"fake-echo"}, Env: map[string]string{"GIT_CONFIG_GLOBAL": "/dev/null"}},
		Seed:     seedBundle(t, "sortie/ONS-7"), StdioInput: []string{"hi\n"},
	})
	if u := usageOf(t, h); !u.Measured || u.InputTokens != 0 || u.OutputTokens != 0 {
		t.Errorf("usage = %+v, want zero, measured", u)
	}
}

func TestUsageReadable(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, nil)
	u := usageOf(t, h)
	if !u.Measured || u.InputTokens != 1200 || u.OutputTokens != 300 || u.CacheReadTokens != 50 || u.CacheWriteTokens != 0 || u.Model != "claude-test" {
		t.Errorf("usage = %+v, want 1200 in, 300 out, 50 cache read, model claude-test, measured", u)
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
