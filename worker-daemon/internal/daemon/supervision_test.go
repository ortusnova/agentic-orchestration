package daemon_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
)

// The workbench shows each job's step as the job reports it
// (agent-platform/run-supervision): the job writes its step to the file
// SORTIE_STEP_FILE names, and each heartbeat carries the last one.
func TestTheStepAJobReportsReachesTheHeartbeat(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_STEPS": "seed-vm-create,seed-vm-recreate", "AGENT_DELAY": "700ms"})
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })

	var steps []string
	for _, b := range h.fake.Beats("ONS-7", 1) {
		if b.Step != "" && (len(steps) == 0 || steps[len(steps)-1] != b.Step) {
			steps = append(steps, b.Step)
			if b.StepAt == nil || b.StepAt.IsZero() {
				t.Errorf("step %q has no time", b.Step)
			}
		}
	}
	if !slices.Equal(steps, []string{"seed-vm-create", "seed-vm-recreate"}) {
		t.Errorf("steps on the heartbeats = %q, want seed-vm-create then seed-vm-recreate: %s", steps, h.describe())
	}
	m := h.machine("ONS-7", 1)
	env := strings.Join(m.Execs()[len(m.Execs())-1].Env, "\n")
	for _, e := range m.Execs() {
		if slices.Contains(e.Argv, "fake-agent") {
			env = strings.Join(e.Env, "\n")
		}
	}
	if !strings.Contains(env, "SORTIE_STEP_FILE="+machine.GuestShare+"/step") {
		t.Errorf("the agent is not told where its step file is:\n%s", env)
	}
}

func TestReadStep(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "step")
	if b := daemon.ReadStep(path); b.Step != "" || b.StepAt != nil {
		t.Errorf("a missing step file reports %+v", b)
	}
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("build\nseed-vm-recreate\n\n")
	if b := daemon.ReadStep(path); b.Step != "seed-vm-recreate" || b.StepAt == nil {
		t.Errorf("the last line is the step: %+v", b)
	}
	write(`{"step": "seed-vm-recreate", "phase": "live_proof"}` + "\n")
	if b := daemon.ReadStep(path); b.Step != "seed-vm-recreate" || b.Phase != "live_proof" {
		t.Errorf("a JSON line gives the step and phase: %+v", b)
	}
	write("bad\x1b[31mstep" + strings.Repeat("x", 300))
	if b := daemon.ReadStep(path); len([]rune(b.Step)) != daemon.MaxStep || strings.ContainsRune(b.Step, '\x1b') {
		t.Errorf("a step is kept short and printable: %q", b.Step)
	}
	write("{not json")
	if b := daemon.ReadStep(path); b.Step != "" {
		t.Errorf("a broken JSON line reports no step: %+v", b)
	}
}

// Scenario: A machine Sortie did not start. A sandbox created by hand
// runs on the worker; the check-in lists it with its name and age, and
// none of the machines a Sortie started.
func TestAMachineSortieDidNotStartIsDeclared(t *testing.T) {
	defer func(d time.Duration) { daemon.ListEvery = d }(daemon.ListEvery)
	daemon.ListEvery = 0
	s := defaults()
	s.noStart = true
	h := newHarness(t, s)
	created := time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)
	h.backend.AddForeign(machine.Listed{Name: "brian-scratch", Labels: map[string]string{}, Created: created, State: "running"})
	h.backend.AddForeign(machine.Listed{Name: "sortie-ONS-34", Labels: map[string]string{machine.LabelSortieManaged: "true"}})
	h.backend.AddForeign(machine.Listed{Name: "no-time"})
	h.start()
	h.offer("ONS-7", 1, map[string]string{"AGENT_DELAY": "1s"})
	h.waitFor("the job's machine", 20*time.Second, func() bool { return h.fake.Count("heartbeat", "ONS-7", 1) > 0 })
	h.waitFor("a check-in while the job runs", 20*time.Second, func() bool {
		d, _ := h.fake.Declaration("taichi")
		return d.Running == 1
	})

	d, _ := h.fake.Declaration("taichi")
	var names []string
	for _, u := range d.Unmanaged {
		names = append(names, u.Name)
	}
	if !slices.Equal(names, []string{"brian-scratch", "no-time"}) {
		t.Fatalf("unmanaged = %q, want the two machines no Sortie started", names)
	}
	if !d.Unmanaged[0].Since.Equal(created) || d.Unmanaged[0].State != "running" {
		t.Errorf("brian-scratch = %+v, want its creation time and state", d.Unmanaged[0])
	}
	if d.Unmanaged[1].Since.IsZero() || time.Since(d.Unmanaged[1].Since) > time.Minute {
		t.Errorf("a machine smolvm gives no time for is aged from when the daemon first saw it: %+v", d.Unmanaged[1])
	}
	if d.Used.CPU != 2 || d.Used.MemGiB != 4 || d.Used.DiskGiB != 10 {
		t.Errorf("used = %+v, want the running job's share", d.Used)
	}

	h.backend.RemoveForeign("brian-scratch")
	h.waitFor("the removed machine to leave the check-in", 20*time.Second, func() bool {
		d, _ := h.fake.Declaration("taichi")
		return len(d.Unmanaged) == 1 && d.Unmanaged[0].Name == "no-time"
	})
}

func TestParseSmolvmList(t *testing.T) {
	out := []byte(`[
	  {"name": "wd-ONS-7-1", "state": "running", "labels": {"worker-daemon.job": "ONS-7"}},
	  {"name": "scratch", "status": "stopped", "created_at": "2026-11-02T18:00:00Z"},
	  {"name": "sortie-ONS-34", "labels": {"sortie.managed": "true"}, "created_at": 1793642400}
	]`)
	got, err := machine.ParseList(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Unmanaged() || !got[1].Unmanaged() || got[2].Unmanaged() {
		t.Fatalf("listed = %+v", got)
	}
	if got[1].State != "stopped" || !got[1].Created.Equal(time.Date(2026, 11, 2, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("scratch = %+v", got[1])
	}
	if got[2].Created.Unix() != 1793642400 {
		t.Errorf("a creation time in Unix seconds = %v", got[2].Created)
	}
	if _, err := machine.ParseList([]byte("not json")); err == nil {
		t.Error("a broken list is an error")
	}
}
