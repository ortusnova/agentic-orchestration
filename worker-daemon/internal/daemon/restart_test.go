package daemon_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// Scenario: Sortie restarts while a job's agent is mid-turn. The stdio
// stream breaks without the C frame and the control plane answers 502
// for a while. The daemon keeps the job's machine, stops the agent,
// dials the stream again once Sortie is back, and starts the agent again
// in the same workspace; the job then ends with one result and one
// report, as if nothing happened.
func TestARestartingControlPlaneKeepsTheJob(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "hold-first"}, func(j *fakecp.Job) { j.KeepInputOpen = true })
	h.waitFor("the agent to hold its turn", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "holding")
	})

	h.fake.Restart("ONS-7", 1)
	h.waitFor("a request answered while Sortie is away", 10*time.Second, func() bool {
		return h.fake.Count("down", "", 0) > 2
	})
	m := h.machine("ONS-7", 1)
	if !m.StoppedAt().IsZero() || h.fake.Count("report", "ONS-7", 1) != 0 {
		t.Fatalf("the machine stopped or the job reported while Sortie restarted: %s", h.describe())
	}
	h.fake.Up()

	h.waitFor("the report", 30*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	if n := h.fake.Streams("ONS-7", 1); n != 2 {
		t.Errorf("stdio streams opened %d, want the first and one after the restart: %s", n, h.describe())
	}
	if n := len(h.backend.Machines()); n != 1 {
		t.Errorf("machines created %d, want the job's one", n)
	}
	var agents int
	for _, e := range m.Execs() {
		if len(e.Argv) > 1 && e.Argv[1] == "fake-agent" {
			agents++
		}
	}
	if agents != 2 {
		t.Errorf("agent starts %d, want two in the same machine", agents)
	}
	if out := h.fake.StdioOutput("ONS-7", 1); strings.Count(out, "agent got: hello agent\n") != 2 || !strings.Contains(out, "done\n") {
		t.Errorf("stdio output %q, want the prompt twice and the work done after the restart", out)
	}
	if code, ok := h.fake.Exit("ONS-7", 1); !ok || code != 0 {
		t.Errorf("exit = %d, %v; want X with status 0", code, ok)
	}
	if _, ok := h.fake.Result("ONS-7", 1); !ok {
		t.Error("no result returned")
	}
	reports := h.reports("ONS-7", 1)
	if len(reports) != 1 || reports[0].Outcome != protocol.OutcomeSucceeded {
		raw, _ := json.Marshal(reports)
		t.Errorf("reports %s, want one success", raw)
	}
}
