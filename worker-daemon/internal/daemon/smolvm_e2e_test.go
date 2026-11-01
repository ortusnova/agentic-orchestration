package daemon_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
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
	for _, kind := range []string{"seed", "stdio", "egress", "mcp", "bundle", "usage"} {
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
