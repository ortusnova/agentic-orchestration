package daemon_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

const testVMKey = "AGE-SECRET-KEY-1TESTFIXTURE"

// imageInputs is a host with homelab's inputs for testImage: a read-only
// folder, a per-job folder, the vm key in a file, a guest kernel, and a
// setup script that writes what it saw into the workspace.
func imageInputs(t *testing.T, setupScript string) (setup, string) {
	t.Helper()
	host := t.TempDir()
	for _, d := range []string{"sources", "recorder"} {
		if err := os.MkdirAll(filepath.Join(host, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	key := filepath.Join(host, "keys.txt")
	if err := os.WriteFile(key, []byte(testVMKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := defaults()
	s.images = map[string]any{testImage: map[string]any{
		"user": "builder", "lib_dir": "/opt/homelab-kernel",
		"mounts": []map[string]any{
			{"host": filepath.Join(host, "sources"), "guest": "/home/builder/.cache/homelab-sources", "read_only": true},
			{"host": filepath.Join(host, "recorder"), "guest": "/var/log/homelab-recorder", "per_job": true},
		},
		"env":       map[string]string{"NODE_EXTRA_CA_CERTS": "/etc/egress-ca.crt"},
		"env_files": map[string]string{"HOMELAB_VM_AGE_KEY": key},
		"setup":     []string{setupScript},
	}}
	return s, host
}

func (h *harness) offerNested(id string) {
	self, _ := os.Executable()
	h.fake.Offer(fakecp.Job{
		ID: id, Attempt: 1,
		Requires: protocol.Requires{Capabilities: []string{"smolvm", "nested"}, CPU: 8, MemGiB: 20, DiskGiB: 100},
		Run: protocol.Run{Backend: "smolvm", Image: testImage, Command: self, Args: []string{"fake-agent"},
			Env: map[string]string{"SORTIE_ISSUE_BRANCH": "sortie/" + id, "GIT_CONFIG_GLOBAL": "/dev/null"}},
		Seed:       seedBundle(h.t, "sortie/"+id),
		StdioInput: []string{"hello agent\n"},
	})
}

// A job of an image with host inputs gets them as the co-located homelab
// machines did: nested virtualization when it requires nested, the
// image's user and guest kernel, its shared folders (a per-job one that
// outlives the job), and the vm key in every guest command's environment,
// setup's and the agent's, never in the daemon's log.
func TestImageInputsReachTheJob(t *testing.T) {
	s, host := imageInputs(t, `printf %s "$HOMELAB_VM_AGE_KEY" > setup-saw.txt`)
	s.host.CPUs, s.host.MemAvailableGiB = 16, 64
	h := newHarness(t, s)
	h.offerNested("ONS-7")
	res := resultOf(t, h)
	if len(res.Bundle) == 0 {
		t.Error("no bundle came back")
	}
	if r := h.reports("ONS-7", 1); len(r) != 1 || r[0].Outcome != protocol.OutcomeSucceeded {
		t.Fatalf("reports = %+v, want one success", r)
	}
	m := h.machine("ONS-7", 1)
	spec := m.Spec()
	if !spec.Nested || spec.User != "builder" || !slices.Equal(spec.HostEnv, []string{"SMOLVM_LIB_DIR=/opt/homelab-kernel"}) {
		t.Errorf("spec: nested %v, user %q, host env %q", spec.Nested, spec.User, spec.HostEnv)
	}
	want := []machine.Mount{
		{Host: filepath.Join(host, "sources"), Guest: "/home/builder/.cache/homelab-sources", ReadOnly: true},
		{Host: filepath.Join(host, "recorder", "ONS-7"), Guest: "/var/log/homelab-recorder"},
	}
	if !slices.Equal(spec.Mounts, want) {
		t.Errorf("mounts = %+v\nwant %+v", spec.Mounts, want)
	}
	if fi, err := os.Stat(filepath.Join(host, "recorder", "ONS-7")); err != nil || !fi.IsDir() {
		t.Errorf("the job's recorder folder is gone after the job: %v", err)
	}
	if b, _ := os.ReadFile(m.HostPath("/workspace/setup-saw.txt")); string(b) != testVMKey {
		t.Errorf("setup saw HOMELAB_VM_AGE_KEY = %q", b)
	}
	execs := m.Execs()
	setupAt, agentAt := -1, -1
	for i, e := range execs {
		if len(e.Argv) == 3 && e.Argv[0] == "sh" && strings.Contains(e.Argv[2], "setup-saw") {
			setupAt = i
		}
		if len(e.Argv) > 1 && e.Argv[1] == "fake-agent" {
			agentAt = i
			if !slices.Contains(e.Env, "NODE_EXTRA_CA_CERTS=/etc/egress-ca.crt") {
				t.Error("the agent's environment has no NODE_EXTRA_CA_CERTS")
			}
			if !slices.Contains(e.Env, "HOMELAB_VM_AGE_KEY="+testVMKey) {
				t.Error("the agent's environment has no HOMELAB_VM_AGE_KEY")
			}
		}
	}
	if setupAt < 0 || agentAt < 0 || setupAt > agentAt {
		t.Errorf("setup at %d, agent at %d: want setup before the agent", setupAt, agentAt)
	}
	if strings.Contains(h.logs.String(), testVMKey) {
		t.Error("the daemon logged the vm key")
	}
}

// A job of an image without host inputs, or one that does not require
// nested, gets none of them.
func TestNoInputsWithoutTheImage(t *testing.T) {
	s, _ := imageInputs(t, "true")
	s.images = map[string]any{"other.smolmachine": s.images[testImage]}
	h := newHarness(t, s)
	h.offer("ONS-7", 1, nil)
	resultOf(t, h)
	spec := h.machine("ONS-7", 1).Spec()
	if spec.Nested || len(spec.Mounts) != 0 || len(spec.HostEnv) != 0 || spec.User != "" {
		t.Errorf("spec carries another image's inputs: %+v", spec)
	}
	for _, e := range h.machine("ONS-7", 1).Execs() {
		for _, kv := range e.Env {
			if strings.HasPrefix(kv, "HOMELAB_VM_AGE_KEY=") {
				t.Errorf("%v has the vm key", e.Argv)
			}
		}
	}
}

// A setup script that fails fails the job before the agent starts, with
// its standard error in the reason, and the machine is deleted.
func TestFailedSetupFailsTheJob(t *testing.T) {
	s, _ := imageInputs(t, `echo "no swap here" >&2; exit 4`)
	s.host.CPUs, s.host.MemAvailableGiB = 16, 64
	h := newHarness(t, s)
	h.offerNested("ONS-7")
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	r := h.reports("ONS-7", 1)
	if len(r) != 1 || r[0].Outcome != protocol.OutcomeFailed || !strings.Contains(r[0].Reason, "setup 1") ||
		!strings.Contains(r[0].Reason, "no swap here") || r[0].Machine != protocol.MachineDeleted {
		t.Errorf("reports = %+v, want one failure naming setup 1 and its error, machine deleted", r)
	}
	for _, e := range h.machine("ONS-7", 1).Execs() {
		if len(e.Argv) > 1 && e.Argv[1] == "fake-agent" {
			t.Error("the agent ran after a failed setup")
		}
	}
}

// A host that offers nested virtualization declares it, unless its
// configuration withholds it: a worker without the inputs of the images
// that need nested takes none of their jobs.
func TestNestedDeclaration(t *testing.T) {
	host := hostinfo.Snapshot{OS: "linux", Arch: "amd64", KVM: true, Nested: true, CPUs: 8, MemAvailableGiB: 40, DiskAvailableGiB: 200}
	for _, tc := range []struct {
		nested any
		want   bool
	}{{nil, true}, {true, true}, {false, false}} {
		s := defaults()
		s.noStart = true
		if tc.nested != nil {
			s.nested = tc.nested
		}
		h := newHarness(t, s)
		if d := h.d.Declaration(host); d.Nested != tc.want {
			t.Errorf("smolvm.nested %v: declared nested %v, want %v", tc.nested, d.Nested, tc.want)
		}
	}
}
