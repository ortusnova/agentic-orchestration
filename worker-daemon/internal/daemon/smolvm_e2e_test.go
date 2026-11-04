package daemon_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/hostinfo"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// e2eAgent is the agent of the smolvm end-to-end run, a shell script so
// that any pack with sh, git and socat runs it: it answers its prompt
// with its environment, makes one model request through the egress
// proxy and one tool call through the MCP forwarder, and commits.
const e2eAgent = `read -r line
echo "agent got: $line"
env
echo "cmdline: $0 $*"
printf 'CONNECT model.example:443 HTTP/1.1\r\nHost: model.example:443\r\n\r\nPOST /v1/messages HTTP/1.1\r\nHost: model.example\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}' | socat -t5 - TCP:127.0.0.1:47801 >/dev/null
printf 'POST / HTTP/1.1\r\nHost: mcp\r\nContent-Type: application/json\r\nContent-Length: 2\r\nConnection: close\r\n\r\n{}' | socat -t5 - TCP:127.0.0.1:47802 >/dev/null
git -c user.name=agent -c user.email=agent@example.invalid commit -q --allow-empty -m 'agent work'
echo done
`

// The smolvm end-to-end run: one job from offer to report in a real
// machine, against the fake control plane. It needs KVM, smolvm, and
// a pack with sh, git and socat in WORKER_DAEMON_E2E_PACK.
func TestSmolvmEndToEnd(t *testing.T) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipped: this host has no /dev/kvm, so smolvm cannot start a machine here")
	}
	bin, err := exec.LookPath("smolvm")
	if err != nil {
		t.Skip("skipped: /dev/kvm exists but smolvm is not on PATH")
	}
	pack := os.Getenv("WORKER_DAEMON_E2E_PACK")
	if pack == "" {
		t.Skip("skipped: set WORKER_DAEMON_E2E_PACK to a .smolmachine pack with sh, git and socat (smol-builder's base layer has them)")
	}
	s := defaults()
	s.backend = &machine.Smolvm{Binary: bin}
	s.pack = pack
	s.leaseMS = 30000
	s.usage = []string{"sh", "-c", `echo '{"input_tokens":7,"output_tokens":2}'`}
	h := newHarness(t, s)
	h.fake.Offer(fakecp.Job{
		ID: "ONS-7", Attempt: 1,
		Requires: protocol.Requires{CPU: 1, MemGiB: 1, DiskGiB: 2},
		Run:      protocol.Run{Backend: "smolvm", Image: testImage, Command: "sh", Args: []string{"-c", e2eAgent}, Env: map[string]string{"SORTIE_ISSUE_IDENTIFIER": "ONS-7"}},
		Seed:     seedBundle(t, "sortie/ONS-7"), StdioInput: []string{"hello agent\n"},
	})
	var tokens map[string]string
	h.waitFor("the report", 5*time.Minute, func() bool {
		if tokens == nil {
			if tk := h.fake.Tokens("ONS-7", 1); len(tk) > 0 {
				tokens = tk
			}
		}
		return h.fake.Count("report", "ONS-7", 1) > 0
	})
	ev := h.fake.Events()
	report := indexOf(ev, 0, is("report", "ONS-7", 1))
	for _, kind := range []string{"seed", "stdio", "stdio-X", "egress", "mcp", "usage", "result"} {
		if i := indexOf(ev, 0, is(kind, "ONS-7", 1)); i < 0 || i > report {
			t.Errorf("no %s event before the report: %s", kind, h.describe())
		}
	}
	out := h.fake.StdioOutput("ONS-7", 1)
	if !strings.Contains(out, "agent got: hello agent") || !strings.Contains(out, "done") {
		t.Errorf("stdio output = %q", out)
	}
	for flow, tok := range tokens {
		if strings.Contains(out, tok) {
			t.Errorf("the %s token is in the guest's environment or command line", flow)
		}
	}
	if r := h.reports("ONS-7", 1); len(r) != 1 || r[0].Outcome != protocol.OutcomeSucceeded || r[0].Machine != protocol.MachineDeleted {
		t.Errorf("reports = %+v", r)
	}
}

// e2eEgressCheck connects to $ALLOWED and to $DENIED on port 443 from
// the job machine, through its own interface (no proxy), and prints
// whether each connection was made. A nested script, when given, does
// the same from a VM nested in the machine.
const e2eEgressCheck = `read -r line
probe() { if socat -T10 /dev/null "TCP:$1:443,connect-timeout=10" 2>/dev/null; then echo "$2: reached"; else echo "$2: refused"; fi; }
probe "$ALLOWED" "machine allowed"
probe "$DENIED" "machine denied"
if [ -n "$NESTED_SCRIPT" ]; then printf '%s\n' "$NESTED_SCRIPT" > /tmp/nested-egress.sh && sh /tmp/nested-egress.sh; fi
git -c user.name=agent -c user.email=agent@example.invalid commit -q --allow-empty -m 'agent work'
echo done
`

// The smolvm egress end-to-end run (design D8): a job machine made from
// the offer's run.egress reaches a declared host and is refused an
// undeclared one, and so does a VM nested in it, through the machine's
// interface; the refusals are in the state directory after the machine
// is deleted. It needs KVM with nested guests, smolvm, a pack with sh,
// git and socat in WORKER_DAEMON_E2E_PACK, and the network. With
// WORKER_DAEMON_E2E_NESTED_SCRIPT, a shell script run in the machine
// that boots a nested VM (libvirt, as homelab's seed) and prints
// "nested allowed: reached" or "nested allowed: refused", then the same
// for "nested denied", for the hosts in $ALLOWED and $DENIED, it checks
// the nested VM too; its pack (homelab's) goes in WORKER_DAEMON_E2E_PACK.
func TestSmolvmEgressEndToEnd(t *testing.T) {
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("skipped: this host has no /dev/kvm, so smolvm cannot start a machine here")
	}
	host, err := hostinfo.Host{}.Snapshot(t.TempDir())
	if err != nil || !host.Nested {
		t.Skipf("skipped: this host's KVM offers no nested guests (%v), so a VM cannot nest in the job machine", err)
	}
	bin, err := exec.LookPath("smolvm")
	if err != nil {
		t.Skip("skipped: /dev/kvm exists but smolvm is not on PATH")
	}
	pack := os.Getenv("WORKER_DAEMON_E2E_PACK")
	if pack == "" {
		t.Skip("skipped: set WORKER_DAEMON_E2E_PACK to a .smolmachine pack with sh, git and socat (smol-builder's base layer has them)")
	}
	allowed, denied := envOr("WORKER_DAEMON_E2E_ALLOWED", "ghcr.io"), envOr("WORKER_DAEMON_E2E_DENIED", "pastebin.com")
	var nested string
	if f := os.Getenv("WORKER_DAEMON_E2E_NESTED_SCRIPT"); f != "" {
		b, err := os.ReadFile(f) // #nosec G304 -- the tester names the file
		if err != nil {
			t.Fatal(err)
		}
		nested = string(b)
	}
	s := defaults()
	s.backend = &machine.Smolvm{Binary: bin}
	s.pack = pack
	s.leaseMS = 30000
	s.usage = []string{"sh", "-c", `echo '{"input_tokens":7,"output_tokens":2}'`}
	h := newHarness(t, s)
	requires := protocol.Requires{CPU: 2, MemGiB: 4, DiskGiB: 10}
	if nested != "" {
		requires.Capabilities = []string{daemon.CapabilityNested}
	}
	h.fake.Offer(fakecp.Job{
		ID: "ONS-30", Attempt: 1, Requires: requires,
		Run: protocol.Run{Backend: "smolvm", Image: testImage, Command: "sh", Args: []string{"-c", e2eEgressCheck},
			Env: map[string]string{"SORTIE_ISSUE_IDENTIFIER": "ONS-30", "ALLOWED": allowed, "DENIED": denied, "NESTED_SCRIPT": nested}},
		Egress: &protocol.Egress{Repository: &protocol.EgressPolicy{Hosts: []protocol.EgressHost{{Host: allowed, Reason: "the declared host"}}}},
		Seed:   seedBundle(t, "sortie/ONS-30"), StdioInput: []string{"go\n"},
	})
	h.waitFor("the report", 15*time.Minute, func() bool { return h.fake.Count("report", "ONS-30", 1) > 0 })
	out := h.fake.StdioOutput("ONS-30", 1)
	want := []string{"machine allowed: reached", "machine denied: refused"}
	if nested != "" {
		want = append(want, "nested allowed: reached", "nested denied: refused")
	} else {
		t.Log("WORKER_DAEMON_E2E_NESTED_SCRIPT is not set: the nested VM is not checked")
	}
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("stdio output lacks %q: %s", w, out)
		}
	}
	if r := h.reports("ONS-30", 1); len(r) != 1 || r[0].Machine != protocol.MachineDeleted {
		t.Errorf("reports = %+v, want one with the machine deleted", r)
	}
	b, err := os.ReadFile(h.d.RefusalsFile("ONS-30", 1))
	if err != nil || !strings.Contains(string(b), denied) {
		t.Errorf("refusal record = %s, %v; want a refusal of %s", b, err, denied)
	}
	t.Logf("refusal record:\n%s", b)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
