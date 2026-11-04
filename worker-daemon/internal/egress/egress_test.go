package egress

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

const valid = `{
  "source": {"repository": "ortusnova/homelab", "commit": "0123abc"},
  "harness": {"version": 1, "hosts": [{"host": "models.opencode.ai", "reason": "the harness's models"}]},
  "repository": {"version": 1,
    "hosts": [
      {"host": "*.githubusercontent.com", "reason": "release downloads"},
      {"host": "models.opencode.ai", "reason": "named twice"},
      {"host": "api.anthropic.com", "reason": "a credential host, never allowed directly"}
    ],
    "udp": [{"cidr": "0.0.0.0/0", "port": 123, "reason": "NTP"}],
    "private": [{"cidr": "192.168.122.0/24", "reason": "the nested VM's network"}]
  },
  "credential_hosts": ["api.anthropic.com"]
}`

// sortieOffer is run.egress as Sortie's workflow reference shows it for
// a homelab job (agent-platform/job-egress-policy): no version, the
// source's four members, every list present, and the port a string.
const sortieOffer = `{
  "source": {"repository": "homelab", "branch": "main", "commit": "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "file": ".sortie/egress.yaml"},
  "harness": {"hosts": [{"host": "models.opencode.ai", "reason": "the agent harness reaches its models"}], "udp": [], "private": []},
  "repository": {"hosts": [{"host": "deb.debian.org", "reason": "apt installs the build's packages"}], "udp": [{"cidr": "0.0.0.0/0", "port": "123", "reason": "NTP"}], "private": []},
  "credential_hosts": ["api.anthropic.com"]
}`

func TestParseSortiesOffer(t *testing.T) {
	e, err := Parse(json.RawMessage(sortieOffer))
	if err != nil {
		t.Fatal(err)
	}
	p := PlanOf(e, nil)
	if n := p.Network(); n == nil || !slices.Equal(n.Hosts, []string{"models.opencode.ai", "deb.debian.org"}) || !slices.Equal(n.UDP, []string{"0.0.0.0/0:123"}) {
		t.Errorf("network = %+v", n)
	}
}

func TestParseAndPlan(t *testing.T) {
	e, err := Parse(json.RawMessage(valid))
	if err != nil {
		t.Fatal(err)
	}
	p := PlanOf(e, nil)
	want := []Host{
		{Name: "models.opencode.ai", Sources: []string{SourceHarness, SourceRepository}},
		{Name: "*.githubusercontent.com", Sources: []string{SourceRepository}},
	}
	if !slices.EqualFunc(p.Hosts, want, func(a, b Host) bool { return a.Name == b.Name && slices.Equal(a.Sources, b.Sources) }) {
		t.Errorf("hosts = %+v, want %+v", p.Hosts, want)
	}
	if !slices.Equal(p.UDP, []string{"0.0.0.0/0:123"}) || !slices.Equal(p.Private, []string{"192.168.122.0/24"}) {
		t.Errorf("udp = %v, private = %v", p.UDP, p.Private)
	}
	if !slices.Equal(p.Withheld, []string{"api.anthropic.com"}) {
		t.Errorf("withheld = %v, want api.anthropic.com", p.Withheld)
	}
	if got := p.NoProxy(); !slices.Equal(got, []string{"models.opencode.ai", ".githubusercontent.com"}) {
		t.Errorf("NO_PROXY hosts = %v", got)
	}
	n := p.Network()
	if n == nil || !slices.Equal(n.Hosts, []string{"models.opencode.ai", "*.githubusercontent.com"}) {
		t.Errorf("network = %+v", n)
	}
}

// A credential host is never allowed directly: not by its name, not by a
// pattern that covers it, and not when only the run's credentials name it.
func TestCredentialHostsAreWithheld(t *testing.T) {
	e := &protocol.Egress{Repository: &protocol.EgressPolicy{Hosts: []protocol.EgressHost{
		{Host: "*.anthropic.com"}, {Host: "api.openai.com"}, {Host: "ghcr.io"},
	}}, CredentialHosts: []string{"api.anthropic.com"}}
	p := PlanOf(e, []string{"api.openai.com"})
	if len(p.Hosts) != 1 || p.Hosts[0].Name != "ghcr.io" {
		t.Errorf("hosts = %+v, want only ghcr.io", p.Hosts)
	}
	if !slices.Equal(p.Withheld, []string{"*.anthropic.com", "api.openai.com"}) {
		t.Errorf("withheld = %v", p.Withheld)
	}
}

// A policy that allows nothing once credential hosts are withheld gives
// no network, never an open one.
func TestAPlanThatAllowsNothingHasNoNetwork(t *testing.T) {
	e := &protocol.Egress{Harness: &protocol.EgressPolicy{Hosts: []protocol.EgressHost{{Host: "api.anthropic.com"}}},
		CredentialHosts: []string{"api.anthropic.com"}}
	if n := PlanOf(e, nil).Network(); n != nil {
		t.Errorf("network = %+v, want none", n)
	}
}

func TestAbsentIsNoPolicy(t *testing.T) {
	for _, raw := range []string{"", " ", "null"} {
		e, err := Parse(json.RawMessage(raw))
		if e != nil || err != nil {
			t.Errorf("Parse(%q) = %v, %v; want nil, nil", raw, e, err)
		}
	}
}

// Scenario: Invalid. Each malformed policy is refused with an error that
// names the entry.
func TestParseRefusesAMalformedPolicy(t *testing.T) {
	repo := func(member string) string {
		return `{"repository": {"version": 1, "hosts": [{"host": "ghcr.io", "reason": "r"}], ` + member + `}}`
	}
	for _, c := range []struct{ raw, want string }{
		{`{"repository": {"version": 1, "hosts": [{"host": "*", "reason": "everything"}]}}`, `run.egress.repository.hosts[0].host: "*"`},
		{`{"repository": {"version": 1, "hosts": [{"host": "*.com", "reason": "a suffix"}]}}`, `run.egress.repository.hosts[0].host: "*.com"`},
		{`{"harness": {"version": 1, "hosts": [{"host": "ok.example", "reason": "r"}, {"host": "10.0.0.1", "reason": "an address"}]}}`, `run.egress.harness.hosts[1].host`},
		{`{"repository": {"version": 1, "hosts": [{"host": "a.*.example", "reason": "r"}]}}`, `run.egress.repository.hosts[0].host`},
		{`{"repository": {"version": 1, "hosts": [{"host": "GHCR.io", "reason": "r"}]}}`, `run.egress.repository.hosts[0].host`},
		{`{"repository": {"version": 2, "hosts": []}}`, `run.egress.repository.version`},
		{repo(`"udp": [{"cidr": "0.0.0.0/0", "port": 0, "reason": "r"}]`), `run.egress.repository.udp[0].port`},
		{repo(`"udp": [{"cidr": "0.0.0.0/0", "port": "9-3", "reason": "r"}]`), `run.egress.repository.udp[0].port`},
		{repo(`"udp": [{"cidr": "1.2.3.4/8", "port": 53, "reason": "r"}]`), `run.egress.repository.udp[0].cidr`},
		{repo(`"private": [{"cidr": "8.8.8.0/24", "reason": "r"}]`), `run.egress.repository.private[0].cidr`},
		{repo(`"private": [{"cidr": "10.0.0.0/7", "reason": "r"}]`), `run.egress.repository.private[0].cidr`},
		{repo(`"allow_all": true`), `unknown field "allow_all"`},
		{`{"credential_hosts": ["api.anthropic.com"]}`, `neither a harness nor a repository`},
		{`{"repository": {"version": 1, "hosts": []}, "credential_hosts": ["*"]}`, `run.egress.credential_hosts[0]`},
		{`{"repository": {"version": 1, "hosts": []}} {}`, `data after the object`},
		{`"open"`, `run.egress`},
		{repo(`"private": [{"cidr": "169.254.0.0/16", "reason": "link-local"}]`), `run.egress.repository.private[0].cidr`},
		{repo(`"private": [{"cidr": "100.64.0.0/10", "reason": "not a private range of the schema"}]`), `run.egress.repository.private[0].cidr`},
		{repo(`"private": [{"cidr": "10.0.0.0/8", "reason": " "}]`), `run.egress.repository.private[0].reason`},
		{`{"harness": {"hosts": [{"host": "ghcr.io", "reason": ""}]}}`, `run.egress.harness.hosts[0].reason`},
		{repo(`"udp": [{"cidr": "0.0.0.0/0", "port": "123"}]`), `run.egress.repository.udp[0].reason`},
		{`{"harness": {"hosts": [{"host": "ghcr.io:443", "reason": "r"}]}}`, `run.egress.harness.hosts[0].host`},
		{`{"harness": {"hosts": [{"host": "1.2.3.4", "reason": "r"}]}}`, `run.egress.harness.hosts[0].host`},
		{`{"source": {"repository": "homelab", "url": "https://x"}, "harness": {"hosts": []}}`, `unknown field "url"`},
	} {
		_, err := Parse(json.RawMessage(c.raw))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%s) = %v, want an error naming %s", c.raw, err, c.want)
		}
	}
}

func TestPorts(t *testing.T) {
	e, err := Parse(json.RawMessage(`{"repository": {"version": 1, "hosts": [], "udp": [{"cidr": "10.0.0.0/8", "port": "5000-5010", "reason": "r"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := PlanOf(e, nil)
	if !slices.Equal(p.UDP, []string{"10.0.0.0/8:5000-5010"}) {
		t.Errorf("udp = %v", p.UDP)
	}
	if n := p.Network(); n != nil {
		t.Errorf("a plan with UDP and no host gives network %+v, want none: smolvm would leave the internet open", n)
	}
	b, _ := json.Marshal(protocol.EgressUDP{CIDR: "0.0.0.0/0", Port: "123"})
	if !strings.Contains(string(b), `"port":"123"`) {
		t.Errorf("a single port marshals as %s, want a string, as Sortie sends it", b)
	}
	if _, err := Parse(json.RawMessage(`{"harness": {"hosts": [{"host": "intranet", "reason": "a single-label name"}]}}`)); err != nil {
		t.Errorf("a single-label name: %v", err)
	}
}
