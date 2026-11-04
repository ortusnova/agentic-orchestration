package daemon_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/netip"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// scenarioEgress is the policy of the scenario "The flags": a harness
// host, a repository pattern, NTP, and a credential host, which the
// repository policy also names.
func scenarioEgress() *protocol.Egress {
	return &protocol.Egress{
		Source:  &protocol.EgressSource{Repository: "homelab", Branch: "main", Commit: "0123abc", File: ".sortie/egress.yaml"},
		Harness: &protocol.EgressPolicy{Hosts: []protocol.EgressHost{{Host: "models.opencode.ai", Reason: "the harness's models"}}},
		Repository: &protocol.EgressPolicy{
			Hosts: []protocol.EgressHost{
				{Host: "*.githubusercontent.com", Reason: "release downloads"},
				{Host: "api.anthropic.com", Reason: "named, but a credential host"},
			},
			UDP: []protocol.EgressUDP{{CIDR: "0.0.0.0/0", Port: "123", Reason: "NTP"}},
		},
		CredentialHosts: []string{"api.anthropic.com"},
	}
}

// Scenario: The flags. The offer's run.egress becomes the machine's
// network, and its create arguments are those of designs D8 and D15,
// every private range denied, with nothing that allows the credential
// host.
//
// Scenario: A model request. NO_PROXY holds the hosts allowed directly,
// so the credential host and an undeclared host still go to the egress
// forward.
func TestTheOffersPolicyIsTheMachinesNetwork(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, map[string]string{
		"AGENT_MODEL_HOST":    "api.anthropic.com",
		"AGENT_DIRECT_HOSTS":  "models.opencode.ai,raw.githubusercontent.com",
		"AGENT_PROXIED_HOSTS": "api.anthropic.com,pastebin.com,githubusercontent.com.evil.example",
	}, func(j *fakecp.Job) { j.Egress = scenarioEgress() })
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	if r := h.reports("ONS-7", 1); len(r) != 1 || r[0].Outcome != protocol.OutcomeSucceeded {
		t.Fatalf("reports = %+v, want one success", r)
	}

	spec := h.machine("ONS-7", 1).Spec()
	want := &machine.Network{Hosts: []string{"models.opencode.ai", "*.githubusercontent.com"}, UDP: []string{"0.0.0.0/0:123"}}
	if n := spec.Network; n == nil || !slices.Equal(n.Hosts, want.Hosts) || !slices.Equal(n.UDP, want.UDP) || len(n.Private) != 0 {
		t.Fatalf("network = %+v, want %+v", n, want)
	}
	args, err := machine.CreateArgs(spec)
	if err != nil {
		t.Fatal(err)
	}
	line := " " + strings.Join(args, " ") + " "
	for _, w := range []string{" --net-backend virtio-net ", " --block-private ",
		" --allow-host-pattern models.opencode.ai ", " --allow-host-pattern *.githubusercontent.com ", " --allow-udp 0.0.0.0/0:123 "} {
		if !strings.Contains(line, w) {
			t.Errorf("create args lack%s: %s", w, line)
		}
	}
	if got := flagValues(args, "--deny-cidr"); !slices.Equal(got, d15) {
		t.Errorf("--deny-cidr = %v, want design D15's ranges %v", got, d15)
	}
	for _, a := range args {
		if a == "--allow-host" || a == "--net" || strings.Contains(a, "anthropic") {
			t.Errorf("create args carry %s: %s", a, line)
		}
	}
	for _, f := range []string{protocol.FlowEgress, protocol.FlowMCP} {
		if !slices.ContainsFunc(spec.Forwards, func(fw machine.Forward) bool { return fw.Name == f }) {
			t.Errorf("the %s forward is gone: %+v", f, spec.Forwards)
		}
	}

	out := h.fake.StdioOutput("ONS-7", 1)
	for _, w := range []string{"direct: models.opencode.ai", "direct: raw.githubusercontent.com", "proxied: api.anthropic.com", "proxied: pastebin.com"} {
		if !strings.Contains(out, w) {
			t.Errorf("the agent did not print %q: %s", w, out)
		}
	}
	var viaEgress bool
	for _, e := range h.fake.Events() {
		if e.Kind == "egress" && e.Job == "ONS-7" && bytes.HasPrefix(e.Body, []byte("POST api.anthropic.com:443/v1/messages")) {
			viaEgress = true
		}
	}
	if !viaEgress {
		t.Errorf("the model request to api.anthropic.com did not reach the egress flow: %s", h.describe())
	}
	var env []string
	for _, x := range h.machine("ONS-7", 1).Execs() {
		if slices.Contains(x.Argv, "fake-agent") {
			env = x.Env
		}
	}
	for _, k := range []string{"NO_PROXY", "no_proxy"} {
		if v := k + "=127.0.0.1,localhost,models.opencode.ai,.githubusercontent.com"; !slices.Contains(env, v) {
			t.Errorf("the agent's environment lacks %s: %v", v, env)
		}
	}

	logs := h.logs.String()
	for _, w := range []string{
		"host=models.opencode.ai source=harness",
		"host=*.githubusercontent.com source=repository",
		"host left on the egress flow",
	} {
		if !strings.Contains(logs, w) {
			t.Errorf("the job's log lacks %q", w)
		}
	}
}

// Scenario: Absent. An offer with no run.egress gets a machine with no
// network, as before.
func TestNoPolicyNoNetwork(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, nil)
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	spec := h.machine("ONS-7", 1).Spec()
	if spec.Network != nil {
		t.Fatalf("network = %+v, want none", spec.Network)
	}
	args, err := machine.CreateArgs(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		if strings.HasPrefix(a, "--net") || strings.HasPrefix(a, "--allow-") || strings.HasPrefix(a, "--deny-") || a == "--block-private" {
			t.Errorf("create args carry %s", a)
		}
	}
	if _, err := os.Stat(h.d.RefusalsFile("ONS-7", 1)); !os.IsNotExist(err) {
		t.Errorf("a machine with no network has a refusal record: %v", err)
	}
}

// Scenario: Invalid. A malformed run.egress gets no machine and one
// failed report that names the entry.
func TestAMalformedPolicyGetsNoMachine(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, nil, func(j *fakecp.Job) {
		j.Run.Egress = json.RawMessage(`{"repository": {"version": 1, "hosts": [{"host": "*", "reason": "everything"}]}}`)
	})
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	time.Sleep(300 * time.Millisecond)
	r := h.reports("ONS-7", 1)
	if len(r) != 1 || r[0].Outcome != protocol.OutcomeFailed || r[0].Machine != protocol.MachineDeleted ||
		!strings.Contains(r[0].Reason, `run.egress.repository.hosts[0].host: "*"`) {
		t.Fatalf("reports = %+v, want one failure naming run.egress.repository.hosts[0].host", r)
	}
	if m := h.machine("ONS-7", 1); m != nil {
		t.Error("a machine was made for a malformed policy")
	}
	if n := len(h.backend.Machines()); n != 0 {
		t.Errorf("%d machines, want none", n)
	}
}

// The fake control plane serves run.egress in the form Sortie sends:
// its members, each policy's lists never null, and a UDP port as a
// string.
func TestTheFakeServesRunEgress(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, nil, func(j *fakecp.Job) { j.Egress = scenarioEgress() })
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	var raw []byte
	for _, e := range h.fake.Events() {
		if e.Kind == "poll" && e.Job == "ONS-7" {
			raw = e.Body
		}
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"source", "harness", "repository", "credential_hosts"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("run.egress lacks %s: %s", k, raw)
		}
	}
	if !strings.Contains(string(raw), `"port":"123"`) {
		t.Errorf("run.egress = %s, want the UDP port as a string", raw)
	}
	if strings.Contains(string(raw), "null") || strings.Contains(string(raw), `"version"`) {
		t.Errorf("run.egress = %s, want lists never null and no version", raw)
	}
	src, _ := doc["source"].(map[string]any)
	for _, k := range []string{"repository", "branch", "commit", "file"} {
		if _, ok := src[k]; !ok {
			t.Errorf("run.egress.source lacks %s: %s", k, raw)
		}
	}
}

// A job a restarted daemon cannot adopt keeps its machine's refusals
// too, and keeps its network when it is reopened.
func TestAnAdoptedMachineKeepsItsRefusals(t *testing.T) {
	h := newHarness(t, restartSetup())
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "hold-first"}, func(j *fakecp.Job) {
		j.KeepInputOpen = true
		j.Egress = scenarioEgress()
	})
	h.waitFor("the agent to hold its turn", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "holding")
	})
	h.backend.Refuse(daemon.MachineName("ONS-7", 1), "pastebin.com")
	h.stop()
	h.fake.NoResume = true
	h.startNewDaemon()
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	if !h.machine("ONS-7", 1).Deleted() {
		t.Fatal("the machine was not deleted")
	}
	b, err := os.ReadFile(h.d.RefusalsFile("ONS-7", 1))
	if err != nil || !strings.Contains(string(b), `"pastebin.com"`) {
		t.Errorf("refusal record = %s, %v; want pastebin.com", b, err)
	}
}

// Scenario: After the machine. A refusal of ONS-30 attempt 1 is in the
// daemon's state directory after its machine is deleted.
func TestRefusalsAreKeptWithTheRun(t *testing.T) {
	h := newHarness(t, defaults())
	h.backend.Refuse(daemon.MachineName("ONS-30", 1), "pastebin.com")
	h.offerJob("ONS-30", 1, nil, func(j *fakecp.Job) { j.Egress = scenarioEgress() })
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-30", 1) > 0 })
	if !h.machine("ONS-30", 1).Deleted() {
		t.Fatal("the machine was not deleted")
	}
	b, err := os.ReadFile(h.d.RefusalsFile("ONS-30", 1))
	if err != nil {
		t.Fatalf("no refusal record after the machine: %v", err)
	}
	var rec daemon.Refusals
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Job != "ONS-30" || rec.Attempt != 1 || !strings.Contains(string(rec.Events), `"pastebin.com"`) {
		t.Errorf("refusal record = %s, want pastebin.com for ONS-30 attempt 1", b)
	}
}

// d15 is design D15's ranges, in its order.
var d15 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "fe80::/10", "::1/128"}

// flagValues is the value of each flag in args, in order.
func flagValues(args []string, flag string) []string {
	var out []string
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// Scenario: A declared private range. The entry is allowed by
// --allow-cidr and left out of the deny set, every other address of
// 192.168.0.0/16 is denied, and --allow-private is never passed.
func TestADeclaredPrivateRange(t *testing.T) {
	h := newHarness(t, defaults())
	h.offerJob("ONS-7", 1, nil, func(j *fakecp.Job) {
		j.Egress = &protocol.Egress{Repository: &protocol.EgressPolicy{
			Hosts:   []protocol.EgressHost{{Host: "ghcr.io", Reason: "images"}},
			Private: []protocol.EgressPrivate{{CIDR: "192.168.1.0/30", Reason: "the lab's switch"}},
		}}
	})
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	args, err := machine.CreateArgs(h.machine("ONS-7", 1).Spec())
	if err != nil {
		t.Fatal(err)
	}
	if got := flagValues(args, "--allow-cidr"); !slices.Equal(got, []string{"192.168.1.0/30"}) {
		t.Errorf("--allow-cidr = %v, want 192.168.1.0/30", got)
	}
	if slices.Contains(args, "--allow-private") {
		t.Errorf("create args carry --allow-private: %v", args)
	}
	entry := netip.MustParsePrefix("192.168.1.0/30")
	lan := netip.MustParsePrefix("192.168.0.0/16")
	denied := new(big.Int)
	for _, d := range flagValues(args, "--deny-cidr") {
		p := netip.MustParsePrefix(d)
		if p.Overlaps(entry) {
			t.Errorf("--deny-cidr %s covers an address of 192.168.1.0/30", d)
		}
		if lan.Contains(p.Addr()) {
			denied.Add(denied, new(big.Int).Lsh(big.NewInt(1), uint(32-p.Bits())))
		}
	}
	if want := big.NewInt(1<<16 - 4); denied.Cmp(want) != 0 {
		t.Errorf("--deny-cidr covers %s addresses of 192.168.0.0/16, want %s, every one but the entry's", denied, want)
	}
	if !strings.Contains(h.logs.String(), "egress: private ranges denied") {
		t.Error("the job's log does not name the denied ranges")
	}
}

// Scenario: Loopback stays denied. A private entry of loopback,
// link-local or a public range gets no machine and one failed report
// that names the entry.
func TestAPrivateEntryOfLoopbackGetsNoMachine(t *testing.T) {
	for i, cidr := range []string{"127.0.0.0/8", "fe80::/64", "8.8.8.0/24"} {
		t.Run(cidr, func(t *testing.T) {
			h := newHarness(t, defaults())
			id := fmt.Sprintf("ONS-%d", 40+i)
			h.offerJob(id, 1, nil, func(j *fakecp.Job) {
				j.Egress = &protocol.Egress{Repository: &protocol.EgressPolicy{
					Hosts:   []protocol.EgressHost{{Host: "ghcr.io", Reason: "images"}},
					Private: []protocol.EgressPrivate{{CIDR: cidr, Reason: "r"}},
				}}
			})
			h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", id, 1) > 0 })
			time.Sleep(300 * time.Millisecond)
			r := h.reports(id, 1)
			if len(r) != 1 || r[0].Outcome != protocol.OutcomeFailed || !strings.Contains(r[0].Reason, "run.egress.repository.private[0].cidr: "+cidr) {
				t.Fatalf("reports = %+v, want one failure naming %s", r, cidr)
			}
			if n := len(h.backend.Machines()); n != 0 {
				t.Errorf("%d machines, want none", n)
			}
		})
	}
}
