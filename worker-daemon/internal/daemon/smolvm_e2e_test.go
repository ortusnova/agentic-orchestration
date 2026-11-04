package daemon_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/egress"
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
	var id string
	s.workerID, id = e2eNames()
	s.leaseMS = 30000
	s.usage = []string{"sh", "-c", `echo '{"input_tokens":7,"output_tokens":2}'`}
	h := newHarness(t, s)
	h.fake.Offer(fakecp.Job{
		ID: id, Attempt: 1,
		Requires: protocol.Requires{CPU: 1, MemGiB: 1, DiskGiB: 2},
		Run:      protocol.Run{Backend: "smolvm", Image: testImage, Command: "sh", Args: []string{"-c", e2eAgent}, Env: map[string]string{"SORTIE_ISSUE_IDENTIFIER": id}},
		Seed:     seedBundle(t, "sortie/"+id), StdioInput: []string{"hello agent\n"},
	})
	var tokens map[string]string
	h.waitFor("the report", 5*time.Minute, func() bool {
		if tokens == nil {
			if tk := h.fake.Tokens(id, 1); len(tk) > 0 {
				tokens = tk
			}
		}
		return h.fake.Count("report", id, 1) > 0
	})
	ev := h.fake.Events()
	report := indexOf(ev, 0, is("report", id, 1))
	for _, kind := range []string{"seed", "stdio", "stdio-X", "egress", "mcp", "usage", "result"} {
		if i := indexOf(ev, 0, is(kind, id, 1)); i < 0 || i > report {
			t.Errorf("no %s event before the report: %s", kind, h.describe())
		}
	}
	out := h.fake.StdioOutput(id, 1)
	if !strings.Contains(out, "agent got: hello agent") || !strings.Contains(out, "done") {
		t.Errorf("stdio output = %q", out)
	}
	for flow, tok := range tokens {
		if strings.Contains(out, tok) {
			t.Errorf("the %s token is in the guest's environment or command line", flow)
		}
	}
	if r := h.reports(id, 1); len(r) != 1 || r[0].Outcome != protocol.OutcomeSucceeded || r[0].Machine != protocol.MachineDeleted {
		t.Errorf("reports = %+v", r)
	}
}

// e2eEgressCheck connects to $ALLOWED and to $DENIED on port 443 from
// the job machine, through its own interface (no proxy), and prints
// whether each connection was made; with $PRIVATE_NAME, it does the same
// for that name and its address, $PRIVATE_ADDR, on $PRIVATE_PORT. A
// nested script, when given, does the same from a VM nested in the
// machine.
const e2eEgressCheck = `read -r line
probe() { if socat -T10 /dev/null "TCP:$1:${3:-443},connect-timeout=10" 2>/dev/null; then echo "$2: reached"; else echo "$2: refused"; fi; }
probe "$ALLOWED" "machine allowed"
probe "$DENIED" "machine denied"
if [ -n "$PRIVATE_NAME" ]; then
  probe "$PRIVATE_NAME" "machine private name" "$PRIVATE_PORT"
  probe "$PRIVATE_ADDR" "machine private address" "$PRIVATE_PORT"
fi
if [ -n "$NESTED_SCRIPT" ]; then printf '%s\n' "$NESTED_SCRIPT" > /tmp/nested-egress.sh && sh /tmp/nested-egress.sh; fi
git -c user.name=agent -c user.email=agent@example.invalid commit -q --allow-empty -m 'agent work'
echo done
`

// The smolvm egress end-to-end run (designs D8 and D15): a job machine
// made from the offer's run.egress reaches a declared host and is
// refused an undeclared one, and so does a VM nested in it, through the
// machine's interface; the refusals are in the state directory after
// the machine is deleted. It needs KVM with nested guests, smolvm, a
// pack with sh, git and socat in WORKER_DAEMON_E2E_PACK, and the
// network. With WORKER_DAEMON_E2E_NESTED_SCRIPT, a shell script run in
// the machine that boots a nested VM (libvirt, as homelab's seed) and
// prints "nested allowed: reached" or "nested allowed: refused", then
// the same for "nested denied", for the hosts in $ALLOWED and $DENIED,
// it checks the nested VM too; its pack (homelab's) goes in
// WORKER_DAEMON_E2E_PACK.
//
// With WORKER_DAEMON_E2E_PRIVATE_NAME, a name whose answer is a private
// address with a listener on WORKER_DAEMON_E2E_PRIVATE_PORT (443 by
// default), the policy declares that name too, and the machine must be
// refused it by name and by address, both refusals in the record, while
// WORKER_DAEMON_E2E_ALLOWED is still reached (scenario: A declared name
// answers a private address). Run it in a network namespace of its own
// whose resolver gives that answer, with testdata/private-name-netns.sh,
// so that the host's DNS is not changed.
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
	privName, privPort := os.Getenv("WORKER_DAEMON_E2E_PRIVATE_NAME"), envOr("WORKER_DAEMON_E2E_PRIVATE_PORT", "443")
	var privAddr string
	if privName != "" {
		privAddr = privateAnswer(t, privName, privPort)
	}
	s := defaults()
	s.backend = &machine.Smolvm{Binary: bin}
	s.pack = pack
	var id string
	s.workerID, id = e2eNames()
	s.leaseMS = 30000
	s.usage = []string{"sh", "-c", `echo '{"input_tokens":7,"output_tokens":2}'`}
	t.Cleanup(func() {
		// The daemon deletes the machine; this deletes it by name if a
		// failed run left it.
		_ = exec.Command(bin, "machine", "delete", "-f", "--name", daemon.MachineName(id, 1)).Run() // #nosec G204 -- argv, no shell
	})
	h := newHarness(t, s)
	requires := protocol.Requires{CPU: 2, MemGiB: 4, DiskGiB: 10}
	if nested != "" {
		requires.Capabilities = []string{daemon.CapabilityNested}
	}
	hosts := []protocol.EgressHost{{Host: allowed, Reason: "the declared host"}}
	if privName != "" {
		hosts = append(hosts, protocol.EgressHost{Host: privName, Reason: "a declared name that answers a private address"})
	}
	h.fake.Offer(fakecp.Job{
		ID: id, Attempt: 1, Requires: requires,
		Run: protocol.Run{Backend: "smolvm", Image: testImage, Command: "sh", Args: []string{"-c", e2eEgressCheck},
			Env: map[string]string{"SORTIE_ISSUE_IDENTIFIER": id, "ALLOWED": allowed, "DENIED": denied, "NESTED_SCRIPT": nested,
				"PRIVATE_NAME": privName, "PRIVATE_ADDR": privAddr, "PRIVATE_PORT": privPort}},
		Egress: &protocol.Egress{Repository: &protocol.EgressPolicy{Hosts: hosts}},
		Seed:   seedBundle(t, "sortie/"+id), StdioInput: []string{"go\n"},
	})
	h.waitFor("the report", 15*time.Minute, func() bool { return h.fake.Count("report", id, 1) > 0 })
	out := h.fake.StdioOutput(id, 1)
	want := []string{"machine allowed: reached", "machine denied: refused"}
	if privName != "" {
		want = append(want, "machine private name: refused", "machine private address: refused")
	} else {
		t.Log("WORKER_DAEMON_E2E_PRIVATE_NAME is not set: a declared name that answers a private address is not checked")
	}
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
	if r := h.reports(id, 1); len(r) != 1 || r[0].Machine != protocol.MachineDeleted {
		t.Errorf("reports = %+v, want one with the machine deleted", r)
	}
	b, err := os.ReadFile(h.d.RefusalsFile(id, 1))
	if err != nil || !strings.Contains(string(b), denied) {
		t.Errorf("refusal record = %s, %v; want a refusal of %s", b, err, denied)
	}
	if privName != "" {
		if n := refusalsOf(b, privName, privAddr); n < 2 {
			t.Errorf("refusal record = %s; want two refusals of %s (%s), by name and by address, found %d", b, privName, privAddr, n)
		}
	}
	t.Logf("stdio output:\n%s", out)
	t.Logf("refusal record:\n%s", b)
}

// privateAnswer is the address the resolver gives name: a private
// address of design D15, with a listener on port, or the test fails, as
// a refusal would prove nothing.
func privateAnswer(t *testing.T, name, port string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", name)
	if err != nil || len(addrs) == 0 {
		t.Fatalf("WORKER_DAEMON_E2E_PRIVATE_NAME %s does not resolve here: %v", name, err)
	}
	a := addrs[0].Unmap()
	if !slices.ContainsFunc(egress.DeniedRanges, func(r netip.Prefix) bool { return r.Contains(a) }) {
		t.Fatalf("WORKER_DAEMON_E2E_PRIVATE_NAME %s answers %s, which is not a private address of design D15", name, a)
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(a.String(), port), 10*time.Second)
	if err != nil {
		t.Fatalf("nothing listens on %s (%s) port %s, so a refusal would prove nothing: %v", name, a, port, err)
	}
	_ = c.Close()
	return a.String()
}

// refusalsOf counts the egress events in a refusal record that name the
// host name or its address addr.
func refusalsOf(record []byte, name, addr string) int {
	var rec daemon.Refusals
	if json.Unmarshal(record, &rec) != nil {
		return 0
	}
	var events []json.RawMessage
	if json.Unmarshal(rec.Events, &events) != nil {
		var wrapped map[string][]json.RawMessage
		if json.Unmarshal(rec.Events, &wrapped) != nil {
			return strings.Count(string(rec.Events), addr)
		}
		for _, v := range wrapped {
			events = append(events, v...)
		}
	}
	n := 0
	for _, e := range events {
		if s := string(e); strings.Contains(s, addr) || strings.Contains(s, `"`+name+`"`) {
			n++
		}
	}
	return n
}

// e2eNames returns a worker id and a job id of this run alone: on a
// worker host the daemon under test must neither delete the live
// worker's machines as strays nor name a machine as one of its jobs.
func e2eNames() (worker, job string) {
	n := fmt.Sprintf("e2e-%d-%d", os.Getpid(), time.Now().Unix())
	return n, strings.ToUpper(n)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
