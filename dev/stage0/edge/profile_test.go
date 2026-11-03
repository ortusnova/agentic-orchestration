package main

import (
	"encoding/json"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The hosts of the forge credential nono holds besides those the
// workflow binds: git over HTTPS and the API.
var forgeHosts = []string{"github.com", "api.github.com"}

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
	b, err := os.ReadFile("../egress-profile.json")
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
// binds, from WORKFLOW.md's environment.remote. The hosts a job may reach
// are nono's alone (egress-profile.json): the remote kind takes no
// allow_hosts.
func workflowCredentials(t *testing.T) (creds map[string][]string) {
	t.Helper()
	b, err := os.ReadFile("../WORKFLOW.md")
	if err != nil {
		t.Fatal(err)
	}
	creds = map[string][]string{}
	cred := regexp.MustCompile(`^\s+- \{name: ([a-z0-9_]+), .*hosts: \[([^\]]*)\]`)
	section := ""
	for line := range strings.SplitSeq(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "#"):
			continue
		case strings.HasSuffix(trimmed, ":") && !strings.HasPrefix(trimmed, "-"):
			section = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if m := cred.FindStringSubmatch(line); m != nil && section == "credentials" {
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

// nono allows a job only exact hosts and *.domain patterns, among them the
// forge's and those of the credentials it holds, and holds each credential
// the workflow binds, for its hosts, read from its process environment
// (run-nono.sh).
func TestEgressProfileFollowsTheWorkflow(t *testing.T) {
	p := readProfile(t)
	creds := workflowCredentials(t)
	host := regexp.MustCompile(`^(\*\.)?[a-z0-9-]+(\.[a-z0-9-]+)+$`)
	for _, h := range p.Network.AllowDomain {
		if !host.MatchString(h) {
			t.Errorf("nono allows %q, which is neither a host nor a *.domain pattern", h)
		}
	}
	want := slices.Clone(forgeHosts)
	for _, hs := range creds {
		want = append(want, hs...)
	}
	for _, h := range want {
		if !slices.Contains(p.Network.AllowDomain, h) {
			t.Errorf("nono does not allow %s, a host of a credential it holds", h)
		}
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
		if !strings.HasPrefix(c.CredentialKey, "env://STAGE0_EGRESS_") {
			t.Errorf("%s reads its credential from %q, not from run-nono.sh's environment", name, c.CredentialKey)
		}
		u, _ := url.Parse(c.Upstream)
		if !slices.Contains(p.Network.AllowDomain, u.Hostname()) {
			t.Errorf("%s's host %s is not allowed", name, u.Hostname())
		}
	}
}
