package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The hosts of the credentials of egress-profile.json: what a homelab job
// reaches besides the hosts homelab declares.
var credentialHosts = []string{"api.anthropic.com", "api.github.com", "github.com"}

type renderedProfile struct {
	Meta struct {
		Name string `json:"name"`
	} `json:"meta"`
	Network struct {
		AllowDomain       []string                   `json:"allow_domain"`
		Credentials       []string                   `json:"credentials"`
		CustomCredentials map[string]json.RawMessage `json:"custom_credentials"`
	} `json:"network"`
}

// render runs `stage0-edge homelab-profile` on hostsFile and the kit's
// egress profile, and reads what it wrote.
func render(t *testing.T, hostsFile string) (renderedProfile, error) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "egress-profile-homelab.json")
	if err := homelabProfile([]string{"-base", "../egress-profile.json", "-hosts", hostsFile, "-out", out}); err != nil {
		if _, statErr := os.Stat(out); statErr == nil {
			t.Errorf("a refused file still wrote %s", out)
		}
		return renderedProfile{}, err
	}
	var p renderedProfile
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	return p, nil
}

// homelab-on-sortie#The homelab workflow allows only what homelab
// declares: the profile of homelab jobs allows exactly the hosts under
// hosts in egress-hosts.yaml and those of the credentials nono holds, with
// those credentials; none of the kit's own list, and nothing refused.
func TestTheHomelabProfileIsWhatHomelabDeclares(t *testing.T) {
	p, err := render(t, "testdata/egress-hosts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{"auth.docker.io", "deb.debian.org", "ghcr.io", "us-central1-docker.pkg.dev"}, credentialHosts...)
	slices.Sort(want)
	if !slices.Equal(p.Network.AllowDomain, want) {
		t.Errorf("allow_domain = %v\nwant exactly %v", p.Network.AllowDomain, want)
	}
	if p.Meta.Name != HomelabProfileName {
		t.Errorf("meta.name = %q", p.Meta.Name)
	}
	base := readProfile(t)
	if !slices.Equal(p.Network.Credentials, base.Network.Credentials) || len(p.Network.CustomCredentials) != len(base.Network.CustomCredentials) {
		t.Errorf("credentials = %v, want the kit's %v", p.Network.Credentials, base.Network.Credentials)
	}
	for _, h := range []string{"charts.jetstack.io", "pypi.org", "acme-staging-v02.api.letsencrypt.org", "10.17.9.1"} {
		if slices.Contains(p.Network.AllowDomain, h) {
			t.Errorf("the homelab profile allows %s, which homelab does not declare", h)
		}
	}
}

// homelab-on-sortie#The homelab workflow allows only what homelab
// declares, scenario A new host: a host homelab adds to egress-hosts.yaml
// is in the profile the kit renders next.
func TestANewHomelabHostIsAllowed(t *testing.T) {
	b, err := os.ReadFile("testdata/egress-hosts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	added := strings.Replace(string(b), "  - host: ghcr.io\n",
		"  - host: get.helm.sh\n    ports: [443]\n    steps: [seed]\n    reason: The helm release the seed installs.\n  - host: ghcr.io\n", 1)
	file := filepath.Join(t.TempDir(), "egress-hosts.yaml")
	if err := os.WriteFile(file, []byte(added), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := render(t, file)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(p.Network.AllowDomain, "get.helm.sh") {
		t.Errorf("the host homelab added is not allowed: %v", p.Network.AllowDomain)
	}
}

// The kit refuses a missing or malformed egress-hosts.yaml rather than
// render a profile homelab did not declare.
func TestAMissingOrMalformedHostsFileIsRefused(t *testing.T) {
	dir := t.TempDir()
	if _, err := render(t, filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("a missing file was rendered")
	}
	for name, doc := range map[string]string{
		"empty":               "",
		"not YAML":            "hosts: [\n  - host: ghcr.io\n",
		"a list, not a map":   "- host: ghcr.io\n",
		"no hosts":            "refused: []\n",
		"hosts is null":       "hosts:\nrefused: []\n",
		"hosts is a map":      "hosts:\n  host: ghcr.io\n",
		"an entry is a name":  "hosts:\n  - ghcr.io\n",
		"an entry, no host":   "hosts:\n  - ports: [443]\n",
		"a wildcard":          "hosts:\n  - host: '*.docker.io'\n",
		"a URL":               "hosts:\n  - host: https://ghcr.io\n",
		"upper case":          "hosts:\n  - host: GHCR.io\n",
		"a host with a port":  "hosts:\n  - host: ghcr.io:443\n",
		"a host listed twice": "hosts:\n  - host: ghcr.io\n  - host: ghcr.io\n",
	} {
		file := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".yaml")
		if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		if p, err := render(t, file); err == nil {
			t.Errorf("%s: rendered %v", name, p.Network.AllowDomain)
		}
	}
}

// homelabHarness is an edge whose homelab jobs get a nono of their own,
// which allows the hosts of the profile rendered from testdata.
func homelabHarness(t *testing.T) (h *harness, kit, homelab *fakeNono) {
	t.Helper()
	ca := newTestCA(t)
	kit = startFakeNono(t, ca, http.NotFoundHandler())
	kit.allowed["charts.jetstack.io:443"] = true
	homelab = startFakeNono(t, ca, http.NotFoundHandler())
	homelab.pass = kit.pass
	homelab.allowed = map[string]bool{}
	p, err := render(t, "testdata/egress-hosts.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range p.Network.AllowDomain {
		homelab.allowed[host+":443"] = true
	}
	h = newHarness(t, kit.addr, kit.pass)
	h.edge.cfg.Repositories = map[string]string{"homelab": homelab.addr}
	h.sortie.run("ONS-20", "homelab")
	h.sortie.run("ONS-7", "agentic-orchestration")
	return h, kit, homelab
}

// homelab-on-sortie#The homelab workflow allows only what homelab
// declares: a homelab job on taichi reaches a host homelab lists and the
// hosts of its credentials, through the homelab nono, and is refused one
// homelab does not list, even one the kit's profile allows other jobs.
func TestAHomelabJobGetsTheHomelabEgress(t *testing.T) {
	h, kit, homelab := homelabHarness(t)
	tok := h.sortie.mintFor("ONS-20", 1, "egress")
	for host, want := range map[string]int{
		"ghcr.io:443":            http.StatusOK,
		"api.anthropic.com:443":  http.StatusOK,
		"pastebin.com:443":       http.StatusForbidden,
		"charts.jetstack.io:443": http.StatusForbidden,
	} {
		if status := h.connect("taichi", host, tok); status != want {
			t.Errorf("the homelab job's CONNECT to %s answered %d, want %d", host, status, want)
		}
	}
	if kit.connectCount() != 0 || homelab.connectCount() != 4 {
		t.Errorf("connects: kit %d, homelab %d; want 0 and 4", kit.connectCount(), homelab.connectCount())
	}
	if n := h.sortie.calls(); n != 1 {
		t.Errorf("Sortie's state was read %d times for one attempt, want once", n)
	}
	log := h.log.String()
	if !strings.Contains(log, `msg="egress tunnel" worker=taichi job=ONS-20 attempt=1 host=ghcr.io:443 profile=homelab`) {
		t.Errorf("the homelab tunnel is not logged with its profile:\n%s", log)
	}
	if !strings.Contains(log, `msg="egress refused" worker=taichi job=ONS-20 attempt=1 host=pastebin.com:443 profile=homelab status=403`) {
		t.Errorf("the homelab refusal is not logged with its profile:\n%s", log)
	}

	// A job of another repository keeps the kit's egress.
	if status := h.connect("taichi", "charts.jetstack.io:443", h.sortie.mintFor("ONS-7", 1, "egress")); status != http.StatusOK {
		t.Errorf("an agentic-orchestration job's CONNECT answered %d, want 200 through the kit's nono", status)
	}
	if kit.connectCount() != 1 {
		t.Errorf("the kit's nono was reached %d times, want once", kit.connectCount())
	}
}

// While Sortie cannot say which repository a job runs in, its CONNECT
// reaches no nono: it is answered 503, as when Sortie cannot introspect.
func TestAJobOfUnknownRepositoryReachesNoNono(t *testing.T) {
	h, kit, homelab := homelabHarness(t)
	if status := h.connect("taichi", "ghcr.io:443", h.sortie.mintFor("ONS-99", 1, "egress")); status != http.StatusServiceUnavailable {
		t.Errorf("a job Sortie does not list as running: %d, want 503", status)
	}
	h.sortie.mu.Lock()
	h.sortie.stateDown = true
	h.sortie.mu.Unlock()
	if status := h.connect("taichi", "ghcr.io:443", h.sortie.mintFor("ONS-20", 2, "egress")); status != http.StatusServiceUnavailable {
		t.Errorf("while Sortie's state is down: %d, want 503", status)
	}
	if kit.connectCount() != 0 || homelab.connectCount() != 0 {
		t.Errorf("connects: kit %d, homelab %d; want none", kit.connectCount(), homelab.connectCount())
	}
}

func TestRepositoriesConfigValidate(t *testing.T) {
	lp := &LiveProof{Egress: "127.0.0.1:7691", JobsFile: "/x/active"}
	for name, repos := range map[string]map[string]string{
		"no address":              {"homelab": ""},
		"the kit's egress":        {"homelab": "127.0.0.1:7690"},
		"the live-proof egress":   {"homelab": "127.0.0.1:7691"},
		"a name that is not one":  {"home lab": "127.0.0.1:7692"},
		"an empty repository key": {"": "127.0.0.1:7692"},
	} {
		if err := (Config{Egress: "127.0.0.1:7690", LiveProof: lp, Repositories: repos}).Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := (Config{Egress: "127.0.0.1:7690", LiveProof: lp, Repositories: map[string]string{"homelab": "127.0.0.1:7692"}}).Validate(); err != nil {
		t.Errorf("a homelab egress: %v", err)
	}
}
