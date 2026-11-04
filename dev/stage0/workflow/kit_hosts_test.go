package workflow

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// The hosts homelab's jobs reached through nono while the kit's profile
// listed them, before ONS-121. They are homelab's to declare, in its own
// .sortie/egress.yaml: none is a harness host or a credential host.
var homelabHosts = []string{
	"*.pkg.dev", "*.quay.io", "quay.io", "auth.docker.io", "registry-1.docker.io",
	"production.cloudflare.docker.com", "production.cloudfront.docker.com", "ghcr.io",
	"registry.k8s.io", "storage.googleapis.com", "charts.jetstack.io", "charts.longhorn.io",
	"cloudnative-pg.github.io", "kubernetes.github.io",
	"deb.debian.org", "security.debian.org", "cdimage.debian.org",
}

// kitFiles is every file of the stage-0 kit (dev/stage0) and nono's
// profile, without the kit's tests, which name hosts only to refuse them.
func kitFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "sortie-test" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || strings.HasPrefix(d.Name(), "test-") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../../control-plane/nono/egress-profile.json")
	if err != nil {
		t.Fatal(err)
	}
	files["control-plane/nono/egress-profile.json"] = string(b)
	return files
}

// mentionsHost says whether text names host as a host, not as the start
// of an image reference or a path (ghcr.io/nolabs-ai/nono is nono's image
// on p1, not a host a job reaches).
func mentionsHost(text, host string) bool {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9.*-])` + regexp.QuoteMeta(host) + `([^A-Za-z0-9./-]|$)`)
	return re.MatchString(text)
}

// Scenario family "The kit keeps no repository's hosts": no file of the
// kit, nono's profile among them, holds a host that only homelab's jobs
// need, and every host the kit gives a job is a harness host, a
// credential host or a host only a live proof reaches.
func TestTheKitKeepsNoRepositorysHosts(t *testing.T) {
	for path, text := range kitFiles(t) {
		for _, h := range homelabHosts {
			if mentionsHost(text, h) {
				t.Errorf("%s holds %s, a host only homelab's jobs need: it belongs in homelab's .sortie/egress.yaml", path, h)
			}
		}
	}
	known := credentialHosts(workflowCredentials(t))
	for h := range harnessHosts(t) {
		known = append(known, h)
	}
	var live struct {
		AllowDomain []string `json:"allow_domain"`
	}
	b, err := os.ReadFile("../egress-live-proof.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &live); err != nil {
		t.Fatal(err)
	}
	for _, h := range readProfile(t).Network.AllowDomain {
		if !slices.Contains(known, h) {
			t.Errorf("nono allows %s, which is neither a credential host nor a harness host", h)
		}
	}
	for _, h := range live.AllowDomain {
		if slices.Contains(known, h) {
			t.Errorf("egress-live-proof.json allows %s, which every job reaches already", h)
		}
	}
}

// No kit script reads a repository's checkout for its egress: none reads
// an egress list from homelab (its egress-hosts.yaml or .sortie/egress.yaml;
// Sortie alone reads a repository's policy, from its base branch), the
// scripts that run nono, the egress gate and their profiles never name
// homelab or STAGE0_HOMELAB_SRC, and homelab's inputs (homelab-inputs.sh,
// dev-host-apply.sh), which do read its checkout, build no egress list.
func TestNoKitScriptReadsARepositoryForEgress(t *testing.T) {
	egressFile := regexp.MustCompile(`egress-hosts\.ya?ml|\.sortie/egress\.ya?ml|STAGE0_HOMELAB_SRC[^\n]*egress|egress[^\n]*STAGE0_HOMELAB_SRC`)
	for path, text := range kitFiles(t) {
		if !isScript(path) {
			continue
		}
		if m := egressFile.FindString(text); m != "" {
			t.Errorf("%s reads a repository's egress (%q): Sortie gives each job its repository's policy", path, m)
		}
	}
	egress := []string{"run-nono.sh", "egress-credentials.sh", "run-egress-gate.sh", "front-config.sh", "egress-live-proof.json",
		"live-proofs/main.go", "live-proofs/profile.go", "units/ortus-stage0-nono.service", "units/ortus-stage0-nono-live-proof.service",
		"units/ortus-stage0-egress-gate.service"}
	for _, name := range egress {
		b, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.ToLower(string(b)); strings.Contains(s, "homelab") {
			t.Errorf("%s, which runs or builds the egress, names homelab", name)
		}
	}
	for _, name := range []string{"homelab-inputs.sh", "dev-host-apply.sh"} {
		b, err := os.ReadFile(filepath.Join("..", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range []string{"allow_domain", "egress-profile", "nono", "allow-host", "egress.yaml", "egress-hosts"} {
			if strings.Contains(string(b), w) {
				t.Errorf("%s, which reads homelab's checkout, touches the egress (%s)", name, w)
			}
		}
	}
}

// Scenario: Deploy without a homelab checkout. stage0.sh on p1, with no
// STAGE0_HOMELAB_SRC (p1's stage0.env sets none), renders the workflow
// and nono's profile and fetches nothing from homelab: the workflow needs
// no homelab checkout to render, nono's units take only the kit's own
// files, homelab's inputs are read only behind the STAGE0_HOMELAB_SRC
// check (homelab-inputs.sh fetches the operator's checkout on a worker
// that has one), and no kit script clones or fetches homelab's repository.
func TestDeployWithoutAHomelabCheckout(t *testing.T) {
	b, err := os.ReadFile("../stage0.sh")
	if err != nil {
		t.Fatal(err)
	}
	stage0 := string(b)
	wf, err := os.ReadFile("../WORKFLOW.md")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wf), "STAGE0_HOMELAB_SRC") {
		t.Error("WORKFLOW.md needs STAGE0_HOMELAB_SRC to render")
	}
	control := stage0[strings.Index(stage0, "\nif [[ ! -s $ENV_FILE ]]; then"):]
	control, _, _ = strings.Cut(control, "\nEOF\n")
	if strings.Contains(control, "STAGE0_HOMELAB_SRC") {
		t.Error("the control profile's default stage0.env sets STAGE0_HOMELAB_SRC: p1 has no homelab checkout")
	}
	guard := regexp.MustCompile(`if \[\[ -n \$\{STAGE0_HOMELAB_SRC:-\} && -d \$STAGE0_HOMELAB_SRC \]\]; then\n(\s+[^\n]*\n)*?\s+\. "\$KIT/homelab-inputs\.sh"`)
	if n, all := len(guard.FindAllString(stage0, -1)), strings.Count(stage0, `. "$KIT/homelab-inputs.sh"`); n != all || all == 0 {
		t.Errorf("stage0.sh reads homelab's inputs %d times, %d of them behind the STAGE0_HOMELAB_SRC check", all, n)
	}
	nono := regexp.MustCompile(`(?s)nono_inputs=\(([^)]*)\)`).FindStringSubmatch(stage0)
	if nono == nil {
		t.Fatal("stage0.sh lists no inputs of nono")
	}
	if strings.Contains(strings.ToLower(nono[1]), "homelab") {
		t.Errorf("nono's inputs name homelab: %s", nono[1])
	}
	fetch := regexp.MustCompile(`git[^\n]*(clone|fetch|pull|ls-remote)[^\n]*(HOMELAB_REPO_URL|homelab\.git)`)
	for path, text := range kitFiles(t) {
		if isScript(path) {
			if m := fetch.FindString(text); m != "" {
				t.Errorf("%s fetches homelab's repository: %q", path, m)
			}
		}
	}
}

func isScript(path string) bool {
	return strings.HasSuffix(path, ".sh") || strings.HasSuffix(path, ".py") || strings.HasSuffix(path, ".go") ||
		strings.HasSuffix(path, ".service") || strings.HasSuffix(path, ".timer")
}

// agentic-orchestration declares the hosts its own builds and tests reach
// in its .sortie/egress.yaml, each with its reason, as every repository
// does: Go's module proxy and checksum database, PyPI for the workbench.
func TestThisRepositoryDeclaresItsOwnHosts(t *testing.T) {
	b, err := os.ReadFile("../../../.sortie/egress.yaml")
	if err != nil {
		t.Fatal(err)
	}
	hosts := map[string]string{}
	version := false
	for line := range strings.SplitSeq(string(b), "\n") {
		switch trimmed := strings.TrimSpace(line); {
		case trimmed == "" || strings.HasPrefix(trimmed, "#") || trimmed == "hosts:":
		case trimmed == "version: 1":
			version = true
		default:
			m := egressHost.FindStringSubmatch(line)
			if m == nil {
				t.Errorf(".sortie/egress.yaml has a line that is not a {host, reason} entry: %q", line)
				continue
			}
			if !hostPattern.MatchString(m[1]) || strings.TrimSpace(m[2]) == "" {
				t.Errorf(".sortie/egress.yaml: %s needs a host name and a reason", m[1])
			}
			hosts[m[1]] = m[2]
		}
	}
	if !version {
		t.Error(".sortie/egress.yaml is not version 1")
	}
	for _, want := range []string{"proxy.golang.org", "sum.golang.org", "pypi.org", "files.pythonhosted.org"} {
		if _, ok := hosts[want]; !ok {
			t.Errorf(".sortie/egress.yaml does not declare %s", want)
		}
	}
	for _, c := range credentialHosts(workflowCredentials(t)) {
		for h := range hosts {
			if covers(h, c) || covers(c, h) {
				t.Errorf(".sortie/egress.yaml declares %s, which overlaps the credential host %s", h, c)
			}
		}
	}
}
