package pomerium

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/control-plane/internal/workers"
)

var (
	fpP1     = strings.Repeat("a", 64)
	fpTaichi = strings.Repeat("b", 64)
)

func settings() Settings {
	s := Defaults
	s.PublicURL = "https://cp.example.org:8443"
	return s
}

func list() *workers.List {
	return &workers.List{Workers: []workers.Worker{
		{Name: "p1", Fingerprint: fpP1, NotAfter: time.Now(), Flows: workers.Flows},
		{Name: "taichi", Fingerprint: fpTaichi, NotAfter: time.Now(), Flows: []string{"control", "egress"}},
	}}
}

// allowed returns the fingerprints a route's policy allows.
func allowed(t *testing.T, r Route) []string {
	t.Helper()
	b, _ := json.Marshal(r.Policy)
	var p []struct {
		Allow *struct {
			Or []struct {
				ClientCertificate struct {
					Fingerprint []string `json:"fingerprint"`
				} `json:"client_certificate"`
			} `json:"or"`
		} `json:"allow"`
		Deny any `json:"deny"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p) != 1 {
		t.Fatalf("route %s: want one policy block, got %s", r.Name, b)
	}
	if p[0].Allow == nil {
		if p[0].Deny == nil {
			t.Fatalf("route %s neither allows nor denies: %s", r.Name, b)
		}
		return nil
	}
	if len(p[0].Allow.Or) != 1 {
		t.Fatalf("route %s: want one criterion, got %s", r.Name, b)
	}
	return p[0].Allow.Or[0].ClientCertificate.Fingerprint
}

func routes(t *testing.T, l *workers.List) map[string]Route {
	t.Helper()
	cfg, err := Build(settings(), l)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Route{}
	for _, r := range cfg.Routes {
		if _, dup := out[r.Name]; dup {
			t.Fatalf("route %s twice", r.Name)
		}
		out[r.Name] = r
	}
	return out
}

func TestEveryConnectionNeedsTheCA(t *testing.T) {
	cfg, err := Build(settings(), list())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DownstreamMTLS["enforcement"] != "reject_connection" || cfg.DownstreamMTLS["ca_file"] != Defaults.CAFile {
		t.Fatalf("downstream_mtls = %v, want the CA and enforcement reject_connection", cfg.DownstreamMTLS)
	}
}

func TestEachFlowIsItsOwnRoute(t *testing.T) {
	rs := routes(t, list())
	if len(rs) != len(workers.Flows) {
		t.Fatalf("got routes %v, want one per flow %v", rs, workers.Flows)
	}
	for _, f := range workers.Flows {
		r, ok := rs[f]
		if !ok {
			t.Fatalf("no route for %s", f)
		}
		want := []string{fpP1}
		if f == "control" || f == "egress" {
			want = []string{fpP1, fpTaichi}
		}
		if got := allowed(t, r); !slices.Equal(got, want) {
			t.Errorf("route %s allows %v, want %v", f, got, want)
		}
	}
}

func TestPathsReachSortiesFlows(t *testing.T) {
	rs := routes(t, list())
	for flow, want := range map[string][2]string{
		"control": {"/api/v1/workers/", ""},
		"stdio":   {"/stdio/", "/api/v1/flows/stdio/"},
		"bundles": {"/bundles/", "/api/v1/flows/bundles/"},
		"usage":   {"/usage/", "/api/v1/flows/usage/"},
	} {
		r := rs[flow]
		if r.From != "https://cp.example.org:8443" || r.To != "http://10.89.71.10:7680" || r.Prefix != want[0] || r.PrefixRewrite != want[1] {
			t.Errorf("route %s = from %s to %s prefix %s rewrite %s; want %s to Sortie, %v", flow, r.From, r.To, r.Prefix, r.PrefixRewrite, r.From, want)
		}
	}
	if up := rs["stdio"].AllowUpgrades; !slices.Equal(up, []string{"sortie-stdio/2", "sortie-stdio/1"}) {
		t.Errorf("stdio upgrades = %v, want sortie-stdio/2 and sortie-stdio/1, which the worker daemon asks for", up)
	}
	mcp := rs["mcp"]
	if mcp.Prefix != "/mcp/jobs/" || mcp.To != "http://10.89.71.10:7680" {
		t.Errorf("mcp route = %+v", mcp)
	}
	eg := rs["egress"]
	if eg.From != "tcp+https://*" || eg.To != "http://egress-gate:7691" || !eg.PreserveHostHeader {
		t.Errorf("egress route = from %s to %s preserve host %v; want every CONNECT to the gate with its host", eg.From, eg.To, eg.PreserveHostHeader)
	}
}

func TestEveryRouteAssertsTheIdentity(t *testing.T) {
	for name, r := range routes(t, list()) {
		if got := r.SetRequestHeaders["X-Sortie-Worker-Identity"]; got != "${pomerium.client_cert_san_dns}" || len(r.SetRequestHeaders) != 1 {
			t.Errorf("route %s sets %v, want the identity header from the certificate's DNS name", name, r.SetRequestHeaders)
		}
		if r.PassIdentityHeaders {
			t.Errorf("route %s passes Pomerium's identity headers", name)
		}
	}
}

func TestRemovedWorkerReachesNoRoute(t *testing.T) {
	l := list()
	l.Remove("taichi")
	for name, r := range routes(t, l) {
		if slices.Contains(allowed(t, r), fpTaichi) {
			t.Errorf("route %s still allows taichi's certificate", name)
		}
	}
	l.Remove("p1")
	for name, r := range routes(t, l) {
		if got := allowed(t, r); got != nil {
			t.Errorf("route %s allows %v with no worker listed", name, got)
		}
	}
}

func TestMCPRewrite(t *testing.T) {
	r := routes(t, list())["mcp"]
	// Envoy's RE2 substitution, checked here with Go's regexp, which
	// writes the groups as ${1}.
	sub := strings.NewReplacer(`\1`, "${1}", `\2`, "${2}").Replace(r.RegexRewriteSubstitution)
	re := mustRegexp(t, r.RegexRewritePattern)
	for in, want := range map[string]string{
		"/mcp/jobs/ONS-7/attempts/1/mcp":  "/api/v1/flows/mcp/jobs/ONS-7/attempts/1",
		"/mcp/jobs/ONS-8/attempts/12":     "/api/v1/flows/mcp/jobs/ONS-8/attempts/12",
		"/mcp/jobs/ONS-8/attempts/2/a/b/": "/api/v1/flows/mcp/jobs/ONS-8/attempts/2",
	} {
		if !re.MatchString(in) {
			t.Errorf("%s does not match the rewrite", in)
			continue
		}
		if got := re.ReplaceAllString(in, sub); got != want {
			t.Errorf("%s rewrites to %s, want %s", in, got, want)
		}
	}
	if re.MatchString("/mcp/jobs/ONS-7/attempts/x") {
		t.Error("a non-numeric attempt matches")
	}
}

func TestRenderRefusesABadPublicURL(t *testing.T) {
	for _, u := range []string{"", "http://cp.example.org", "https://", "https://u:p@cp.example.org", "https://cp.example.org/x"} {
		s := settings()
		s.PublicURL = u
		if _, err := Build(s, list()); err == nil {
			t.Errorf("public URL %q accepted", u)
		}
	}
}

func TestRenderIsYAMLHeadedJSON(t *testing.T) {
	b, err := Render(settings(), list())
	if err != nil {
		t.Fatal(err)
	}
	head, body, ok := strings.Cut(string(b), "\n")
	if !ok || !strings.HasPrefix(head, "# ") {
		t.Fatalf("no comment line: %q", head)
	}
	var cfg Config
	if err := json.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
}
