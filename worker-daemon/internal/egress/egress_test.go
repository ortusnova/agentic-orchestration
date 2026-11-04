package egress

import (
	"encoding/json"
	"math/big"
	"net/netip"
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

// d15 is design D15's ranges, as the create arguments name them.
var d15 = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "169.254.0.0/16", "127.0.0.0/8", "fc00::/7", "fe80::/10", "::1/128"}

// Scenario: A declared private range. Each private entry is left out of
// the deny set by splitting the range that holds it, and the deny set
// and the entries together are the D15 ranges exactly.
func TestDenySetSplitsTheRangeOfAnEntry(t *testing.T) {
	for _, c := range []struct {
		name    string
		private []string
		// want is the deny set within the entries' ranges; every other
		// D15 range is denied whole.
		want []string
	}{
		{"none", nil, nil},
		{"an IPv4 entry", []string{"192.168.1.0/30"}, []string{
			"192.168.0.0/24", "192.168.1.4/30", "192.168.1.8/29", "192.168.1.16/28", "192.168.1.32/27", "192.168.1.64/26", "192.168.1.128/25",
			"192.168.2.0/23", "192.168.4.0/22", "192.168.8.0/21", "192.168.16.0/20", "192.168.32.0/19", "192.168.64.0/18", "192.168.128.0/17",
		}},
		{"an IPv6 entry", []string{"fd00::/8"}, []string{"fc00::/8"}},
		{"an IPv6 entry deep in its range", []string{"fd12:3456::/32"}, nil},
		{"an entry equal to a whole range", []string{"10.0.0.0/8"}, []string{}},
		{"two entries in one range", []string{"172.16.0.0/16", "172.31.255.0/24"}, []string{
			"172.17.0.0/16", "172.18.0.0/15", "172.20.0.0/14", "172.24.0.0/14", "172.28.0.0/15", "172.30.0.0/16",
			"172.31.0.0/17", "172.31.128.0/18", "172.31.192.0/19", "172.31.224.0/20", "172.31.240.0/21", "172.31.248.0/22", "172.31.252.0/23", "172.31.254.0/24",
		}},
		{"an entry within another", []string{"192.168.0.0/17", "192.168.1.0/30"}, []string{"192.168.128.0/17"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			deny := DenySet(c.private)
			if c.want != nil {
				var holders []netip.Prefix
				for _, e := range c.private {
					holders = append(holders, holder(t, netip.MustParsePrefix(e)))
				}
				var got []string
				for _, d := range deny {
					if slices.ContainsFunc(holders, func(r netip.Prefix) bool { return within(netip.MustParsePrefix(d), r) }) {
						got = append(got, d)
					}
				}
				if !slices.Equal(got, c.want) {
					t.Errorf("deny set within the entries' ranges = %v, want %v", got, c.want)
				}
			}
			for _, r := range d15 {
				if !slices.ContainsFunc(c.private, func(e string) bool { return netip.MustParsePrefix(e).Overlaps(netip.MustParsePrefix(r)) }) && !slices.Contains(deny, r) {
					t.Errorf("deny set %v lacks %s whole", deny, r)
				}
			}
			checkUnion(t, deny, c.private)
		})
	}
}

// holder is the D15 range that holds p.
func holder(t *testing.T, p netip.Prefix) netip.Prefix {
	t.Helper()
	for _, r := range DeniedRanges {
		if within(p, r) {
			return r
		}
	}
	t.Fatalf("%s is in no D15 range", p)
	return p
}

// checkUnion checks that the deny set holds no address of an entry,
// repeats no address, and with the entries covers each D15 range
// exactly.
func checkUnion(t *testing.T, deny, private []string) {
	t.Helper()
	var entries []netip.Prefix
	for _, s := range private {
		p := netip.MustParsePrefix(s)
		if !slices.ContainsFunc(private, func(o string) bool { q := netip.MustParsePrefix(o); return q != p && within(p, q) }) {
			entries = append(entries, p)
		}
	}
	var ds []netip.Prefix
	for _, s := range deny {
		p := netip.MustParsePrefix(s)
		if p.Masked() != p {
			t.Errorf("deny %s has host bits set", s)
		}
		for _, q := range ds {
			if q.Overlaps(p) {
				t.Errorf("deny %s overlaps deny %s", s, q)
			}
		}
		for _, e := range entries {
			if e.Overlaps(p) {
				t.Errorf("deny %s covers an address of the entry %s", s, e)
			}
		}
		ds = append(ds, p)
	}
	all := append(slices.Clone(ds), entries...)
	for _, s := range d15 {
		r := netip.MustParsePrefix(s)
		sum := new(big.Int)
		for _, p := range all {
			switch {
			case within(p, r):
				sum.Add(sum, size(p))
			case p.Overlaps(r):
				t.Errorf("%s straddles %s", p, r)
			}
		}
		if sum.Cmp(size(r)) != 0 {
			t.Errorf("the deny set and the entries hold %s addresses of %s, want %s", sum, r, size(r))
		}
	}
	for _, p := range all {
		if !slices.ContainsFunc(DeniedRanges, func(r netip.Prefix) bool { return within(p, r) }) {
			t.Errorf("%s is outside the D15 ranges", p)
		}
	}
}

func size(p netip.Prefix) *big.Int {
	return new(big.Int).Lsh(big.NewInt(1), uint(p.Addr().BitLen()-p.Bits()))
}

// The D15 ranges are the deny set of a plan with no private entry, and
// a plan's network carries its deny set and its private entries.
func TestANetworkDeniesThePrivateRanges(t *testing.T) {
	if got := DenySet(nil); !slices.Equal(got, d15) {
		t.Errorf("deny set = %v, want %v", got, d15)
	}
	e := &protocol.Egress{Repository: &protocol.EgressPolicy{
		Hosts:   []protocol.EgressHost{{Host: "ghcr.io", Reason: "r"}},
		Private: []protocol.EgressPrivate{{CIDR: "192.168.1.0/30", Reason: "r"}},
	}}
	n := PlanOf(e, nil).Network()
	if n == nil || !slices.Equal(n.Private, []string{"192.168.1.0/30"}) || !slices.Equal(n.Deny, DenySet([]string{"192.168.1.0/30"})) {
		t.Errorf("network = %+v", n)
	}
}

// Scenario: Loopback stays denied. A private entry outside the private
// ranges, or overlapping loopback or link-local, is invalid.
func TestAPrivateEntryOutsideThePrivateRangesIsInvalid(t *testing.T) {
	for _, c := range []struct{ cidr, want string }{
		{"127.0.0.0/8", "overlaps loopback or link-local"},
		{"127.0.0.1/32", "overlaps loopback or link-local"},
		{"fe80::/64", "overlaps loopback or link-local"},
		{"::1/128", "overlaps loopback or link-local"},
		{"169.254.169.254/32", "overlaps loopback or link-local"},
		{"0.0.0.0/0", "overlaps loopback or link-local"},
		{"::/0", "overlaps loopback or link-local"},
		{"8.8.8.0/24", "is not within a private range"},
		{"100.64.0.0/10", "is not within a private range"},
		{"fc00::/6", "overlaps loopback or link-local"},
		{"2001:db8::/32", "is not within a private range"},
	} {
		raw := `{"repository": {"hosts": [{"host": "ghcr.io", "reason": "r"}], "private": [{"cidr": "` + c.cidr + `", "reason": "r"}]}}`
		_, err := Parse(json.RawMessage(raw))
		if err == nil || !strings.Contains(err.Error(), "run.egress.repository.private[0].cidr: "+c.cidr+" "+c.want) {
			t.Errorf("%s: err = %v, want one naming the entry: %s", c.cidr, err, c.want)
		}
	}
}
