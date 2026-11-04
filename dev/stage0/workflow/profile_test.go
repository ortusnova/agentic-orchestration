package workflow

import (
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type egressProfile struct {
	Network struct {
		AllowDomain       []string `json:"allow_domain"`
		Credentials       []string `json:"credentials"`
		CustomCredentials map[string]struct {
			Upstream      string `json:"upstream"`
			CredentialKey string `json:"credential_key"`
		} `json:"custom_credentials"`
	} `json:"network"`
}

func readProfile(t *testing.T) egressProfile {
	t.Helper()
	b, err := os.ReadFile("../../../control-plane/nono/egress-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	var p egressProfile
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	return p
}

// workflowCredentials reads the hosts of each credential p1's workflow
// binds, from WORKFLOW.md's environment.remote.credentials.
func workflowCredentials(t *testing.T) (creds map[string][]string) {
	t.Helper()
	creds = map[string][]string{}
	cred := regexp.MustCompile(`^      - \{name: ([a-z0-9_]+), .*hosts: \[([^\]]*)\]`)
	for _, line := range remoteBlock(t, "credentials") {
		if m := cred.FindStringSubmatch(line); m != nil {
			for h := range strings.SplitSeq(m[2], ",") {
				creds[m[1]] = append(creds[m[1]], strings.TrimSpace(h))
			}
		}
	}
	if len(creds) == 0 {
		t.Fatal("WORKFLOW.md binds no credential")
	}
	return creds
}

// remoteBlock is the lines of environment.remote.<key>, without comments.
func remoteBlock(t *testing.T, key string) []string {
	t.Helper()
	var block []string
	in, remote := false, false
	for _, line := range workflowFrontMatter(t) {
		switch {
		case line == "  remote:":
			remote = true
			continue
		case remote && !strings.HasPrefix(line, "    "):
			remote = false
		}
		if !remote {
			continue
		}
		if strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "     ") {
			in = line == "    "+key+":"
			continue
		}
		if in {
			block = append(block, line)
		}
	}
	return block
}

// egressHost is one {host, reason} entry of an egress policy, in the
// flow form the kit writes them in.
var egressHost = regexp.MustCompile(`^\s+- \{host: "?([^",]+)"?, reason: "([^"]*)"\}$`)

// harnessHosts reads environment.remote.egress, the harness's hosts, with
// their reasons. Every line of its hosts list must be an entry.
func harnessHosts(t *testing.T) map[string]string {
	t.Helper()
	block := remoteBlock(t, "egress")
	if !slices.Contains(block, "      version: 1") || !slices.Contains(block, "      hosts:") {
		t.Fatalf("environment.remote.egress is not a version-1 policy with hosts: %q", block)
	}
	hosts := map[string]string{}
	for _, line := range block {
		if strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") {
			continue
		}
		m := egressHost.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("environment.remote.egress has an entry that is not {host, reason}: %q", line)
			continue
		}
		if _, dup := hosts[m[1]]; dup {
			t.Errorf("environment.remote.egress declares %s twice", m[1])
		}
		hosts[m[1]] = m[2]
	}
	return hosts
}

var hostPattern = regexp.MustCompile(`^(\*\.)?[a-z0-9-]+(\.[a-z0-9-]+)+$`)

// credentialHosts is every host of the credentials the workflow binds.
func credentialHosts(creds map[string][]string) []string {
	var hosts []string
	for _, hs := range creds {
		for _, h := range hs {
			if !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
	}
	slices.Sort(hosts)
	return hosts
}

// Scenario family "nono serves only the credential hosts": nono's profile
// allows exactly the hosts of the credentials the workflow binds and nono
// holds, holds each credential the workflow binds, for its own host, read
// from its process environment (run-nono.sh on p1, the podman secrets in
// the compose project), and holds no credential the workflow does not bind.
func TestNonoServesOnlyTheCredentialHosts(t *testing.T) {
	p := readProfile(t)
	creds := workflowCredentials(t)
	allowed := slices.Clone(p.Network.AllowDomain)
	slices.Sort(allowed)
	if want := credentialHosts(creds); !slices.Equal(allowed, want) {
		t.Errorf("nono allows %q, want exactly the credential hosts %q", allowed, want)
	}
	for name, hs := range creds {
		c, ok := p.Network.CustomCredentials[name]
		if !ok || !slices.Contains(p.Network.Credentials, name) {
			t.Errorf("nono does not hold %s, which the workflow binds", name)
			continue
		}
		u, err := url.Parse(c.Upstream)
		if err != nil || u.Scheme != "https" || !slices.Contains(hs, u.Hostname()) {
			t.Errorf("%s's route goes to %q, not to one of its hosts %q", name, c.Upstream, hs)
		}
	}
	for name, c := range p.Network.CustomCredentials {
		if _, ok := creds[name]; !ok {
			t.Errorf("nono holds %s, which the workflow binds no placeholder for", name)
		}
		if !strings.HasPrefix(c.CredentialKey, "env://CP_EGRESS_") {
			t.Errorf("%s reads its credential from %q, not from the environment run-nono.sh and the compose project give nono", name, c.CredentialKey)
		}
	}
	for _, name := range p.Network.Credentials {
		if _, ok := p.Network.CustomCredentials[name]; !ok {
			t.Errorf("nono lists %s among its credentials but has no route for it", name)
		}
	}
}

// Scenario: A credential host. An agent calls api.github.com through the
// egress flow with the github_api placeholder: the host is nono's, nono
// holds github_api for it, and the worker leaves it on the egress flow
// (no harness host covers it), so nono replaces the placeholder.
// control-plane/internal/egress's TestLiveNono runs it against the real
// nono on p1 (make -C dev/stage0 test-live).
func TestACredentialHost(t *testing.T) {
	p := readProfile(t)
	creds := workflowCredentials(t)
	if !slices.Contains(creds["github_api"], "api.github.com") {
		t.Fatalf("the workflow binds no github_api placeholder for api.github.com: %q", creds)
	}
	if !slices.Contains(p.Network.AllowDomain, "api.github.com") {
		t.Error("nono does not allow api.github.com")
	}
	if c := p.Network.CustomCredentials["github_api"]; c.Upstream != "https://api.github.com" {
		t.Errorf("github_api's route goes to %q, not to api.github.com", c.Upstream)
	}
	for h := range harnessHosts(t) {
		if covers(h, "api.github.com") {
			t.Errorf("the harness host %s covers api.github.com, which then leaves the egress flow", h)
		}
	}
}

// Scenario: Another host. A request through the egress flow that names
// registry.npmjs.org is refused by nono, which does not allow it: it is a
// harness host, which the worker allows directly. The egress gate records
// the refusal (control-plane/internal/egress's TestHostNotAllowed, and
// TestLiveNono against the real nono).
func TestAnotherHostIsRefusedThroughTheEgressFlow(t *testing.T) {
	p := readProfile(t)
	for _, h := range p.Network.AllowDomain {
		if covers(h, "registry.npmjs.org") {
			t.Errorf("nono allows %s, which covers registry.npmjs.org", h)
		}
	}
	if _, ok := harnessHosts(t)["registry.npmjs.org"]; !ok {
		t.Error("registry.npmjs.org is not a harness host: the agent harness cannot reach npm")
	}
}

// The harness hosts are declared once, in environment.remote.egress, each
// with its reason: the hosts nono's records showed every job's harness
// reaching, among them opencode's models, npm and GitHub's downloads, and
// Firecrawl's search for opencode's websearch. None is a credential host
// or covers one, which would take it off the egress flow and its
// credential.
func TestTheHarnessHostsAreDeclaredOnce(t *testing.T) {
	hosts := harnessHosts(t)
	for _, want := range []string{"models.opencode.ai", "registry.npmjs.org", "codeload.github.com", "*.githubusercontent.com", "mcp.firecrawl.dev"} {
		if _, ok := hosts[want]; !ok {
			t.Errorf("environment.remote.egress does not declare %s", want)
		}
	}
	cred := credentialHosts(workflowCredentials(t))
	for h, reason := range hosts {
		if !hostPattern.MatchString(h) {
			t.Errorf("harness host %q is neither a host nor a *.domain pattern", h)
		}
		if strings.TrimSpace(reason) == "" {
			t.Errorf("harness host %s has no reason", h)
		}
		for _, c := range cred {
			if covers(h, c) || covers(c, h) {
				t.Errorf("harness host %s overlaps the credential host %s", h, c)
			}
		}
	}
}

// covers says whether pattern, an exact host or *.domain, covers host.
func covers(pattern, host string) bool {
	if d, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+d)
	}
	return pattern == host
}
