// Package egress reads an offer's run.egress (agent-platform/job-egress-policy)
// and turns it into the job machine's smolvm network (design D8): the
// hosts of the harness and repository policies, each allowed by name,
// their UDP and private entries, and never a credential host, which
// stays on the egress flow.
package egress

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// Version is the policy format version the daemon reads.
const Version = 1

// Sources of an allowed host.
const (
	SourceHarness    = "harness"
	SourceRepository = "repository"
)

// label is one lower-case DNS label; numeric is a label of digits only,
// which may not end a name.
var (
	label   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	numeric = regexp.MustCompile(`^[0-9]+$`)
)

// maxName bounds a host's name, without its *. prefix.
const maxName = 253

// privateRanges are the ranges an entry of private may lie in, as the
// schema states them: never loopback, link-local or a public range.
var privateRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// alwaysDenied are the loopback and link-local ranges: denied whole,
// whatever the policy says.
var alwaysDenied = []netip.Prefix{
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

// DeniedRanges are the ranges of design D15, in its order: a machine
// with a network is denied each of them, less the policy's private
// entries, so that a declared name whose answer is a private address is
// refused.
var DeniedRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("::1/128"),
}

// Parse reads run.egress strictly: an unknown member, a host that is
// neither a DNS name nor *. and a domain, a CIDR or port that does not
// parse, or a private entry outside the private ranges is an error that
// names the entry. An absent or null run.egress is nil, no error.
func Parse(raw json.RawMessage) (*protocol.Egress, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	var e protocol.Egress
	if err := d.Decode(&e); err != nil {
		return nil, fmt.Errorf("run.egress: %w", err)
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("run.egress: data after the object")
	}
	if e.Harness == nil && e.Repository == nil {
		return nil, errors.New("run.egress: names neither a harness nor a repository policy")
	}
	for _, p := range []struct {
		name string
		p    *protocol.EgressPolicy
	}{{SourceHarness, e.Harness}, {SourceRepository, e.Repository}} {
		if p.p == nil {
			continue
		}
		if err := checkPolicy("run.egress."+p.name, p.p); err != nil {
			return nil, err
		}
	}
	for i, h := range e.CredentialHosts {
		if err := checkHost(h); err != nil {
			return nil, fmt.Errorf("run.egress.credential_hosts[%d]: %w", i, err)
		}
	}
	return &e, nil
}

// checkPolicy checks one policy against the schema. An offer carries no
// version; one that does must carry 1.
func checkPolicy(at string, p *protocol.EgressPolicy) error {
	if p.Version != nil && *p.Version != Version {
		return fmt.Errorf("%s.version: %d is not %d", at, *p.Version, Version)
	}
	for i, h := range p.Hosts {
		if err := checkHost(h.Host); err != nil {
			return fmt.Errorf("%s.hosts[%d].host: %w", at, i, err)
		}
		if err := checkReason(h.Reason); err != nil {
			return fmt.Errorf("%s.hosts[%d].reason: %w", at, i, err)
		}
	}
	for i, u := range p.UDP {
		if _, err := cidr(u.CIDR); err != nil {
			return fmt.Errorf("%s.udp[%d].cidr: %w", at, i, err)
		}
		if err := checkPort(string(u.Port)); err != nil {
			return fmt.Errorf("%s.udp[%d].port: %w", at, i, err)
		}
		if err := checkReason(u.Reason); err != nil {
			return fmt.Errorf("%s.udp[%d].reason: %w", at, i, err)
		}
	}
	for i, pr := range p.Private {
		pf, err := cidr(pr.CIDR)
		if err != nil {
			return fmt.Errorf("%s.private[%d].cidr: %w", at, i, err)
		}
		if slices.ContainsFunc(alwaysDenied, pf.Overlaps) {
			return fmt.Errorf("%s.private[%d].cidr: %s overlaps loopback or link-local, which stay denied", at, i, pr.CIDR)
		}
		if !slices.ContainsFunc(privateRanges, func(r netip.Prefix) bool { return within(pf, r) }) {
			return fmt.Errorf("%s.private[%d].cidr: %s is not within a private range", at, i, pr.CIDR)
		}
		if err := checkReason(pr.Reason); err != nil {
			return fmt.Errorf("%s.private[%d].reason: %w", at, i, err)
		}
	}
	return nil
}

// checkReason accepts a reason that says something.
func checkReason(r string) error {
	if strings.TrimSpace(r) == "" {
		return errors.New("is empty: say why the job reaches the entry")
	}
	return nil
}

// checkHost accepts a lower-case DNS name, or *. and a name of two
// labels or more: never *, a wildcard over a top-level domain such as
// *.com, an address, a port, or a wildcard elsewhere.
func checkHost(h string) error {
	wildcard := strings.HasPrefix(h, "*.")
	name := strings.TrimPrefix(h, "*.")
	if name == "" || len(name) > maxName {
		return fmt.Errorf("%q is not a DNS name of 1 to %d characters", h, maxName)
	}
	if _, err := netip.ParseAddr(strings.Trim(name, "[]")); err == nil {
		return fmt.Errorf("%q is an address, not a name", h)
	}
	labels := strings.Split(name, ".")
	for _, l := range labels {
		if !label.MatchString(l) {
			return fmt.Errorf("%q is not a lower-case DNS name, or *. and a domain", h)
		}
	}
	if numeric.MatchString(labels[len(labels)-1]) {
		return fmt.Errorf("%q is an address, not a name", h)
	}
	if wildcard && len(labels) < 2 {
		return fmt.Errorf("%q covers a whole top-level domain", h)
	}
	return nil
}

// cidr parses a CIDR in its canonical form.
func cidr(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return p, fmt.Errorf("%q is not a CIDR", s)
	}
	if p.Masked() != p {
		return p, fmt.Errorf("%q has host bits set; write %s", s, p.Masked())
	}
	return p, nil
}

// within reports whether every address of p is in r.
func within(p, r netip.Prefix) bool { return r.Bits() <= p.Bits() && r.Contains(p.Addr()) }

// DenySet is design D15's deny set for a policy's private entries, which
// [Parse] has checked: each of [DeniedRanges], in order, less the
// entries, the range that holds an entry split into the largest CIDRs
// that hold no address of it, in address order. The union of the deny
// set and the entries is the D15 ranges exactly.
func DenySet(private []string) []string {
	var keep []netip.Prefix
	for _, s := range private {
		if p, err := netip.ParsePrefix(s); err == nil {
			keep = append(keep, p.Masked())
		}
	}
	var out []string
	for _, r := range DeniedRanges {
		for _, p := range subtract(r, keep) {
			out = append(out, p.String())
		}
	}
	return out
}

// subtract is p less every prefix of keep, as the largest CIDRs, in
// address order.
func subtract(p netip.Prefix, keep []netip.Prefix) []netip.Prefix {
	if slices.ContainsFunc(keep, func(k netip.Prefix) bool { return within(p, k) }) {
		return nil
	}
	if !slices.ContainsFunc(keep, p.Overlaps) {
		return []netip.Prefix{p}
	}
	lo, hi := halves(p)
	return append(subtract(lo, keep), subtract(hi, keep)...)
}

// halves splits p, which is shorter than a single address, in two.
func halves(p netip.Prefix) (lo, hi netip.Prefix) {
	bits := p.Bits()
	lo = netip.PrefixFrom(p.Addr(), bits+1)
	if p.Addr().Is4() {
		b := p.Addr().As4()
		b[bits/8] |= 0x80 >> (bits % 8)
		return lo, netip.PrefixFrom(netip.AddrFrom4(b), bits+1)
	}
	b := p.Addr().As16()
	b[bits/8] |= 0x80 >> (bits % 8)
	return lo, netip.PrefixFrom(netip.AddrFrom16(b), bits+1)
}

// checkPort accepts a port, N, or a range, N-M, within 1 to 65535.
func checkPort(s string) error {
	lo, hi, isRange := strings.Cut(s, "-")
	if !isRange {
		hi = lo
	}
	a, errA := strconv.Atoi(lo)
	b, errB := strconv.Atoi(hi)
	if errA != nil || errB != nil || a < 1 || b > 65535 || a > b || strings.HasPrefix(lo, "+") || strings.HasPrefix(hi, "+") {
		return fmt.Errorf("%q is not a port or a range of ports within 1-65535", s)
	}
	return nil
}

// Host is a host the machine reaches directly, and the policies that
// name it.
type Host struct {
	Name    string
	Sources []string
}

// Plan is what a policy gives the machine.
type Plan struct {
	// Hosts are allowed by name, each with --allow-host-pattern.
	Hosts []Host
	// UDP entries are cidr:port, each with --allow-udp.
	UDP []string
	// Private entries are CIDRs, each with --allow-cidr and left out
	// of the deny set (designs D15 and D16).
	Private []string
	// Withheld are the policies' hosts left out because they are, or
	// cover, a credential host.
	Withheld []string
}

// PlanOf is the plan of a parsed policy: the union of the harness and
// repository policies, in order and without repeats, less every host
// that is, or covers, one of credentialHosts (run.egress.credential_hosts
// and the hosts of the run's credentials).
func PlanOf(e *protocol.Egress, credentialHosts []string) Plan {
	var p Plan
	if e == nil {
		return p
	}
	creds := append(slices.Clone(e.CredentialHosts), credentialHosts...)
	for _, src := range []struct {
		name string
		p    *protocol.EgressPolicy
	}{{SourceHarness, e.Harness}, {SourceRepository, e.Repository}} {
		if src.p == nil {
			continue
		}
		for _, h := range src.p.Hosts {
			if slices.ContainsFunc(creds, func(c string) bool { return covers(h.Host, c) || covers(c, h.Host) }) {
				if !slices.Contains(p.Withheld, h.Host) {
					p.Withheld = append(p.Withheld, h.Host)
				}
				continue
			}
			i := slices.IndexFunc(p.Hosts, func(x Host) bool { return x.Name == h.Host })
			switch {
			case i < 0:
				p.Hosts = append(p.Hosts, Host{Name: h.Host, Sources: []string{src.name}})
			case !slices.Contains(p.Hosts[i].Sources, src.name):
				p.Hosts[i].Sources = append(p.Hosts[i].Sources, src.name)
			}
		}
		for _, u := range src.p.UDP {
			if v := u.CIDR + ":" + string(u.Port); !slices.Contains(p.UDP, v) {
				p.UDP = append(p.UDP, v)
			}
		}
		for _, pr := range src.p.Private {
			if !slices.Contains(p.Private, pr.CIDR) {
				p.Private = append(p.Private, pr.CIDR)
			}
		}
	}
	return p
}

// covers reports whether host a matches every name b matches: the same
// name, or a pattern *.d over d itself or a name below d.
func covers(a, b string) bool {
	if a == b {
		return true
	}
	d, ok := strings.CutPrefix(a, "*.")
	if !ok {
		return false
	}
	b = strings.TrimPrefix(b, "*.")
	return b == d || strings.HasSuffix(b, "."+d)
}

// Empty reports whether the plan allows nothing.
func (p Plan) Empty() bool { return len(p.Hosts) == 0 && len(p.UDP) == 0 && len(p.Private) == 0 }

// Network is the machine's network, or nil, no network, for a plan
// that allows no host directly. smolvm 1.32.4 filters by name only
// beside a host pattern: --block-private with only --allow-udp or
// --allow-private leaves the public internet and the LAN open (operator
// guide, "smolvm's filters, as measured"), so a plan's UDP and private
// entries alone give no network. A network denies the private ranges
// less the plan's private entries (design D15): smolvm otherwise lets
// the machine reach any address resolved for an allowed name, private
// or not.
func (p Plan) Network() *machine.Network {
	if len(p.Hosts) == 0 {
		return nil
	}
	n := &machine.Network{UDP: slices.Clone(p.UDP), Private: slices.Clone(p.Private), Deny: DenySet(p.Private)}
	for _, h := range p.Hosts {
		n.Hosts = append(n.Hosts, h.Name)
	}
	return n
}

// NoProxy is the hosts the machine reaches directly as NO_PROXY writes
// them: a pattern *.d as .d.
func (p Plan) NoProxy() []string {
	out := make([]string, 0, len(p.Hosts))
	for _, h := range p.Hosts {
		if d, ok := strings.CutPrefix(h.Name, "*."); ok {
			out = append(out, "."+d)
		} else {
			out = append(out, h.Name)
		}
	}
	return out
}
