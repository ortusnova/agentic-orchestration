package daemon_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// released waits until the daemon released ONS-7's attempt 1: the
// machine is deleted, the job's listeners are gone, and the next
// check-in declares nothing running. It then checks that the attempt
// was reported nothing.
func released(t *testing.T, h *harness) {
	t.Helper()
	m := h.machine("ONS-7", 1)
	h.waitFor("the machine to be deleted", 10*time.Second, m.Deleted)
	h.waitFor("the job to end", 10*time.Second, func() bool { return len(h.d.Listeners()) == 0 })
	since := len(h.fake.Events())
	h.waitFor("a check-in that declares nothing running", 10*time.Second, func() bool {
		for _, e := range h.fake.Events()[since:] {
			var d protocol.Declaration
			if e.Kind == "checkin" && e.Status == http.StatusOK && json.Unmarshal(e.Body, &d) == nil && d.Running == 0 && d.Used.DiskGiB == 0 {
				return true
			}
		}
		return false
	})
	for _, kind := range []string{"report", "result", "usage"} {
		for _, e := range h.fake.Events() {
			if e.Kind == kind && e.Job == "ONS-7" && e.Status != http.StatusGone {
				t.Errorf("sent a %s for the released attempt, answered %d: %s", kind, e.Status, h.describe())
			}
		}
	}
	if !strings.Contains(h.logs.String(), "the control plane no longer wants the job") {
		t.Errorf("the daemon did not log the release:\n%s", h.logs.String())
	}
}

// Scenario: Sortie cancels a job across its restart. The stdio stream
// breaks, and the restarted Sortie has cancelled the attempt: the next
// heartbeat or stdio dial answers 410. The daemon stops the agent,
// deletes the machine, frees what the job reserved and reports nothing.
func TestAJobCancelledAcrossARestartIsReleased(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "hold-first"}, func(j *fakecp.Job) { j.KeepInputOpen = true })
	h.waitFor("the agent to hold its turn", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "holding")
	})
	h.fake.Restart("ONS-7", 1)
	h.waitFor("a request answered while Sortie is away", 10*time.Second, func() bool {
		return h.fake.Count("down", "", 0) > 2
	})
	h.fake.Cancel("ONS-7", 1)
	h.fake.Up()
	released(t, h)
}

// Scenario: Sortie cancels a running job. Its heartbeat answers 410: the
// daemon releases the job within a heartbeat interval and reports
// nothing.
func TestAJobCancelledWhileItRunsIsReleased(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_MODE": "sleep"})
	h.waitFor("the agent to run", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "sleeping")
	})
	h.fake.Cancel("ONS-7", 1)
	released(t, h)
}

// Scenario: an older Sortie keeps the lease of a job but never serves
// its stdio stream again. The daemon dials it again for no longer than a
// lease, then ends the attempt: the machine is deleted and one failure
// is reported, naming the stream.
func TestAStreamNeverServedAgainEndsTheJob(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "hold-first"}, func(j *fakecp.Job) { j.KeepInputOpen = true })
	h.waitFor("the agent to hold its turn", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "holding")
	})
	h.fake.Unserve("ONS-7", 1)
	h.fake.Restart("ONS-7", 1)
	h.fake.Up()
	m := h.machine("ONS-7", 1)
	h.waitFor("the report", 10*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	if !m.Deleted() {
		t.Errorf("the machine was not deleted before the report")
	}
	reports := h.reports("ONS-7", 1)
	if len(reports) != 1 || reports[0].Outcome != protocol.OutcomeFailed || !strings.Contains(reports[0].Reason, "stdio") || reports[0].Machine != protocol.MachineDeleted {
		raw, _ := json.Marshal(reports)
		t.Errorf("reports %s, want one failure naming the stdio stream, its machine deleted", raw)
	}
	if n := h.fake.Count("result", "ONS-7", 1) + h.fake.Count("usage", "ONS-7", 1); n != 0 {
		t.Errorf("sent %d result or usage requests for an attempt whose stream never came back", n)
	}
}

// Scenario: the control plane stays away. No heartbeat is accepted for
// longer than a lease, so Sortie can no longer hold the job: the daemon
// stops the agent and deletes the machine instead of keeping it.
func TestAControlPlaneAwayForALeaseEndsTheJob(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "hold-first"}, func(j *fakecp.Job) { j.KeepInputOpen = true })
	h.waitFor("the agent to hold its turn", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "holding")
	})
	h.fake.Restart("ONS-7", 1)
	m := h.machine("ONS-7", 1)
	h.waitFor("the machine to be deleted", 10*time.Second, m.Deleted)
	h.waitFor("the job to end", 10*time.Second, func() bool { return len(h.d.Listeners()) == 0 })
	if !strings.Contains(h.logs.String(), "no heartbeat accepted") {
		t.Errorf("the daemon did not say why it ended the job:\n%s", h.logs.String())
	}
}
