package daemon_test

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// restartSetup gives the daemon a lease that outlasts its own restart.
func restartSetup() setup {
	s := defaults()
	s.leaseMS = 3000
	return s
}

// holding offers ONS-7 with an agent that holds its first turn and
// waits until it holds it.
func holding(t *testing.T, h *harness) {
	t.Helper()
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "hold-first"}, func(j *fakecp.Job) { j.KeepInputOpen = true })
	h.waitFor("the agent to hold its turn", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "holding")
	})
}

// Scenario: the worker daemon restarts mid-job. It leaves the job's
// machine running and reports nothing as it stops; the next daemon
// adopts the job, renews its lease, dials the stdio stream again and
// starts the agent again in the same machine, and the job reaches its
// report once.
func TestADaemonRestartKeepsTheJob(t *testing.T) {
	h := newHarness(t, restartSetup())
	holding(t, h)
	m := h.machine("ONS-7", 1)
	before := h.fake.Tokens("ONS-7", 1)
	h.stop()
	if !m.StoppedAt().IsZero() || m.Deleted() {
		t.Fatalf("the stopping daemon stopped or deleted the job's machine: %s", h.describe())
	}
	state := readTree(t, h.cfg.StateDir)
	for flow, tok := range before {
		if strings.Contains(state, tok) {
			t.Errorf("the %s token is kept at rest in the state directory", flow)
		}
	}
	if n := h.fake.Count("report", "ONS-7", 1); n != 0 {
		t.Fatalf("the stopping daemon reported the job %d times", n)
	}
	h.startNewDaemon()
	h.waitFor("the report", 30*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	reports := h.reports("ONS-7", 1)
	if len(reports) != 1 || reports[0].Outcome != protocol.OutcomeSucceeded {
		raw, _ := json.Marshal(reports)
		t.Errorf("reports %s, want one success: %s", raw, h.describe())
	}
	if n := len(h.backend.Machines()); n != 1 {
		t.Errorf("machines created %d, want the job's one", n)
	}
	if n := h.fake.Streams("ONS-7", 1); n != 2 {
		t.Errorf("stdio streams %d, want the first and one after the restart", n)
	}
	if _, ok := h.fake.Result("ONS-7", 1); !ok {
		t.Error("no result returned")
	}
	if !m.Deleted() {
		t.Error("the machine was not deleted once the job ended")
	}
	if !strings.Contains(h.logs.String(), "job adopted") {
		t.Errorf("the new daemon did not log the adoption:\n%s", h.logs.String())
	}
	if n := h.fake.Count("resume", "ONS-7", 1); n != 1 {
		t.Errorf("resumes %d, want the adopted job's one", n)
	}
	if !strings.Contains(h.logs.String(), `msg="jobs left by the last start" adopted=1 not_adopted=0`) {
		t.Errorf("the new daemon did not sum up what it adopted:\n%s", h.logs.String())
	}
}

// Scenario: a Sortie that cannot resume an attempt (no resume route).
// The adopted job cannot get its tokens back: the daemon deletes the
// machine and reports the attempt failed once.
func TestADaemonRestartWithoutResumeReportsTheJob(t *testing.T) {
	h := newHarness(t, restartSetup())
	holding(t, h)
	m := h.machine("ONS-7", 1)
	h.stop()
	h.fake.NoResume = true
	h.startNewDaemon()
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	reports := h.reports("ONS-7", 1)
	if len(reports) != 1 || reports[0].Outcome != protocol.OutcomeFailed || !strings.Contains(reports[0].Reason, "resume") {
		raw, _ := json.Marshal(reports)
		t.Errorf("reports %s, want one failure naming the resume", raw)
	}
	if !m.Deleted() {
		t.Error("the machine was not deleted")
	}
}

// Scenario: Sortie dropped the job while the daemon restarted. The new
// daemon adopts it, its heartbeat answers 410, and it releases the job:
// the machine is deleted and nothing is reported.
func TestADaemonRestartReleasesAJobSortieDropped(t *testing.T) {
	h := newHarness(t, restartSetup())
	holding(t, h)
	m := h.machine("ONS-7", 1)
	h.stop()
	h.fake.Cancel("ONS-7", 1)
	h.startNewDaemon()
	h.waitFor("the machine to be deleted", 20*time.Second, m.Deleted)
	h.waitFor("the job to end", 10*time.Second, func() bool { return len(h.d.Listeners()) == 0 })
	for _, e := range h.fake.Events() {
		if e.Kind == "report" && e.Status != http.StatusGone {
			t.Errorf("reported the dropped job: %s", h.describe())
		}
	}
}

// Scenario: the job's machine is gone when the daemon starts again, as
// after a host reboot. The daemon reports the attempt failed once and
// forgets it.
func TestADaemonRestartWithTheMachineGoneReportsTheJob(t *testing.T) {
	h := newHarness(t, restartSetup())
	holding(t, h)
	m := h.machine("ONS-7", 1)
	h.stop()
	if err := m.Delete(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.startNewDaemon()
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	reports := h.reports("ONS-7", 1)
	if len(reports) != 1 || reports[0].Outcome != protocol.OutcomeFailed || reports[0].Machine != protocol.MachineDeleted {
		raw, _ := json.Marshal(reports)
		t.Errorf("reports %s, want one failure with the machine deleted", raw)
	}
	h.waitFor("the job's state to go", 10*time.Second, func() bool {
		_, err := os.Stat(h.d.JobDir("ONS-7", 1))
		return os.IsNotExist(err)
	})
}

// Scenario: a machine this worker's daemon created has no job state, as
// when its state directory was removed. The daemon deletes it at start;
// a machine of another worker, or one no daemon started, is left alone.
func TestADaemonRestartDeletesAMachineWithoutState(t *testing.T) {
	h := newHarness(t, restartSetup())
	ctx := context.Background()
	create := func(name string, labels map[string]string) machine.Machine {
		m, err := h.backend.Create(ctx, machine.Spec{Name: name, Labels: labels})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	create(daemon.MachineName("ONS-9", 1), map[string]string{machine.LabelJob: "ONS-9", machine.LabelWorker: "taichi"})
	create(daemon.MachineName("ONS-8", 1), map[string]string{machine.LabelJob: "ONS-8", machine.LabelWorker: "p1"})
	create("someone-elses", map[string]string{})
	h.restartDaemon()
	orphan := h.backend.Machine(daemon.MachineName("ONS-9", 1))
	h.waitFor("the machine without state to be deleted", 10*time.Second, orphan.Deleted)
	for _, name := range []string{daemon.MachineName("ONS-8", 1), "someone-elses"} {
		if h.backend.Machine(name).Deleted() {
			t.Errorf("deleted %s, which is not this worker's", name)
		}
	}
}

// A new machine carries the worker's name, so the daemon knows its own.
func TestAMachineNamesItsWorker(t *testing.T) {
	h := newHarness(t, restartSetup())
	h.offer("ONS-7", 1, map[string]string{"AGENT_MODE": "sleep"})
	h.waitFor("the machine", 20*time.Second, func() bool { return h.machine("ONS-7", 1) != nil })
	if got := h.machine("ONS-7", 1).Spec().Labels[machine.LabelWorker]; got != "taichi" {
		t.Errorf("label %s = %q, want taichi", machine.LabelWorker, got)
	}
}
