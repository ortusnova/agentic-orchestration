package egress

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// forgeNono stands in for an attempt's nono: it takes CONNECT with the
// gate's nono credential, intercepts TLS, and for each request runs the
// capture its profile names as nono does (Credential, with the scope its
// cache_path_regex derives from the path), then answers with the
// Authorization it would send upstream, or 503 when the capture fails.
type forgeNono struct {
	t       *testing.T
	ca      *testCA
	pass    string
	profile map[string]any
	l       net.Listener
}

func (n *forgeNono) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect || req.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("nono:"+n.pass)) {
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
		return
	}
	_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\n")
	name, _, _ := net.SplitHostPort(req.Host)
	tc := tls.Server(c, &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return n.ca.leaf(name) }})
	inner, err := http.ReadRequest(bufio.NewReader(tc))
	if err != nil {
		return
	}
	route := "github_api"
	if name == "github.com" {
		route = "github_git"
	}
	capture := n.profile["credential_capture"].(map[string]any)[route].(map[string]any)
	cmd := capture["command"].([]any)
	scope := ""
	if m := regexp.MustCompile(capture["cache_path_regex"].(string)).FindStringSubmatch(inner.URL.Path); m != nil {
		scope = m[1]
	}
	var out bytes.Buffer
	if err := Credential(cmd[len(cmd)-2].(string), cmd[len(cmd)-1].(string), scope, &out); err != nil {
		_, _ = io.WriteString(tc, "HTTP/1.1 503 Service Unavailable\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	got := "Bearer " + out.String()
	if route == "github_git" {
		got = "Basic " + base64.StdEncoding.EncodeToString(out.Bytes())
	}
	_, _ = fmt.Fprintf(tc, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(got), got)
}

// forgeHarness is the gate with ForgeTokens, whose attempts' nonos are
// forgeNonos.
type forgeHarness struct {
	*harness
	dir    string
	mu     sync.Mutex
	starts int
	nonos  map[string]*forgeNono
}

func newForgeHarness(t *testing.T) *forgeHarness {
	t.Helper()
	fh := &forgeHarness{dir: filepath.Join(t.TempDir(), "forge"), nonos: map[string]*forgeNono{}}
	fh.harness = newHarnessWith(t, func(h *harness, cfg *Config) {
		h.nono.allow("api.github.com:443")
		h.nono.allow("github.com:443")
		cfg.ForgeTokens = &ForgeTokens{Nono: "nono", CACert: "ca.crt", CAKey: "ca.key", Dir: fh.dir, Capture: []string{"cp-egress", CredentialCommand}}
	})
	fh.g.forge.start = fh.start
	return fh
}

func (fh *forgeHarness) start(a *attemptNono, profile string) (string, func(), error) {
	b, err := os.ReadFile(profile)
	if err != nil {
		return "", nil, err
	}
	n := &forgeNono{t: fh.t, ca: fh.ca, pass: fh.nono.pass}
	if err := json.Unmarshal(b, &n.profile); err != nil {
		return "", nil, err
	}
	n.l, err = net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	go func() {
		for {
			c, err := n.l.Accept()
			if err != nil {
				return
			}
			go n.serve(c)
		}
	}()
	fh.mu.Lock()
	fh.starts++
	fh.nonos[a.key] = n
	fh.mu.Unlock()
	return n.l.Addr().String(), func() { _ = n.l.Close() }, nil
}

func (fh *forgeHarness) send(method, query string, body any, token string) int {
	fh.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, fh.gate.URL+ForgeTokensPath+query, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fh.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (fh *forgeHarness) register(job string, tokens map[string]string) {
	fh.t.Helper()
	if st := fh.send(http.MethodPost, "", ForgeRegistration{Job: job, Attempt: 1, Tokens: tokens}, controlToken); st != http.StatusNoContent {
		fh.t.Fatalf("registering %s: %d, want 204", job, st)
	}
}

// get sends the job's request to url through the gate, carrying the
// placeholder of owner, and returns the status and the Authorization the
// nono that served it would send upstream.
func (fh *forgeHarness) get(egressToken, rawURL, owner string) (int, string) {
	fh.t.Helper()
	req, _ := http.NewRequest(http.MethodGet, rawURL, nil)
	req.Header.Set("Authorization", "Bearer sortie-credential-github-"+owner)
	resp, err := fh.agent(egressToken).Do(req)
	if err != nil {
		fh.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
}

func basicOf(tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("x-access-token:"+tok))
}

// agent-platform/forge-tokens#The egress gate replaces an attempt's
// placeholders with that attempt's tokens, scenario Two attempts at once.
func TestTwoAttemptsEachGetOnlyTheirOwnTokens(t *testing.T) {
	fh := newForgeHarness(t)
	fh.register("ONS-7", map[string]string{"ortusnova": "ghs_seven_ortusnova"})
	fh.register("ONS-8", map[string]string{"ortusnova": "ghs_eight_ortusnova", "briancappello": "ghs_eight_brian"})
	seven, eight, nine := fh.sortie.mint("ONS-7", Flow), fh.sortie.mint("ONS-8", Flow), fh.sortie.mint("ONS-9", Flow)

	for _, c := range []struct {
		name, token, url, owner, want string
	}{
		{"ONS-7 on the API", seven, "https://api.github.com/repos/ortusnova/fleet-vm/pulls", "ortusnova", "Bearer ghs_seven_ortusnova"},
		{"ONS-7, one owner, a path of none", seven, "https://api.github.com/graphql", "ortusnova", "Bearer ghs_seven_ortusnova"},
		{"ONS-7 on git", seven, "https://github.com/ortusnova/fleet-vm.git/info/refs?service=git-receive-pack", "ortusnova", basicOf("ghs_seven_ortusnova")},
		{"ONS-8 ortusnova on the API", eight, "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova", "Bearer ghs_eight_ortusnova"},
		{"ONS-8 briancappello on the API", eight, "https://api.github.com/repos/briancappello/personal-site/issues", "briancappello", "Bearer ghs_eight_brian"},
		{"ONS-8 an organization", eight, "https://api.github.com/orgs/ortusnova/repos", "ortusnova", "Bearer ghs_eight_ortusnova"},
		{"ONS-8 briancappello on git", eight, "https://github.com/briancappello/personal-site.git/info/refs", "briancappello", basicOf("ghs_eight_brian")},
		{"ONS-9 is not registered", nine, "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova", "Bearer sortie-credential-github-ortusnova"},
	} {
		st, got := fh.get(c.token, c.url, c.owner)
		if st != http.StatusOK || got != c.want {
			t.Errorf("%s: %d %q, want 200 %q", c.name, st, got, c.want)
		}
	}
	// Two owners and a path that names neither: no token at all.
	if st, got := fh.get(eight, "https://api.github.com/graphql", "ortusnova"); st != http.StatusServiceUnavailable {
		t.Errorf("ONS-8 /graphql: %d %q, want nono's 503", st, got)
	}
	// Other hosts still go to the shared nono.
	if st, got := fh.get(seven, "https://api.anthropic.com/v1/models", "x"); st != http.StatusOK || strings.Contains(got, "ghs_") {
		t.Errorf("ONS-7 on api.anthropic.com: %d %q, want the shared nono's answer", st, got)
	}
	if seen := fh.nono.seen(); len(seen) != 2 || seen[0] != "api.github.com:443" || seen[1] != "api.anthropic.com:443" {
		t.Errorf("the shared nono saw %v, want only ONS-9's GitHub CONNECT and ONS-7's model CONNECT", seen)
	}
	log := fh.log.String()
	for _, want := range []string{
		`msg="egress tunnel" worker=p1 job=ONS-7 attempt=1 host=api.github.com:443 profile=forge-tokens`,
		`msg="egress tunnel" worker=p1 job=ONS-8 attempt=1 host=github.com:443 profile=forge-tokens`,
		`msg="egress tunnel" worker=p1 job=ONS-9 attempt=1 host=api.github.com:443` + "\n",
		`msg="GitHub tokens registered" job=ONS-8 attempt=1 owners=briancappello,ortusnova`,
	} {
		if !strings.Contains(log, want) {
			t.Errorf("the gate's log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "ghs_") {
		t.Errorf("the gate logged a token:\n%s", log)
	}
}

// A refresh rewrites the attempt's tokens: its next request carries the
// new one, from the same nono.
func TestARefreshReachesTheNextRequest(t *testing.T) {
	fh := newForgeHarness(t)
	fh.register("ONS-7", map[string]string{"ortusnova": "ghs_first"})
	tok := fh.sortie.mint("ONS-7", Flow)
	if _, got := fh.get(tok, "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova"); got != "Bearer ghs_first" {
		t.Fatalf("before the refresh: %q", got)
	}
	fh.register("ONS-7", map[string]string{"ortusnova": "ghs_second"})
	if _, got := fh.get(tok, "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova"); got != "Bearer ghs_second" {
		t.Fatalf("after the refresh: %q, want the new token", got)
	}
	if fh.starts != 1 {
		t.Errorf("the refresh started %d nonos, want the first only", fh.starts)
	}
	if !strings.Contains(fh.log.String(), `msg="GitHub tokens refreshed" job=ONS-7 attempt=1 owners=ortusnova`) {
		t.Errorf("the refresh is not logged:\n%s", fh.log)
	}
}

// agent-platform/forge-tokens#The egress gate replaces an attempt's
// placeholders with that attempt's tokens, scenario After the attempt:
// nothing of the attempt is left on disk, and its placeholder goes to the
// shared nono unreplaced.
func TestUnregisteringLeavesNothing(t *testing.T) {
	fh := newForgeHarness(t)
	fh.register("ONS-7", map[string]string{"ortusnova": "ghs_seven", "briancappello": "ghs_brian"})
	fh.register("ONS-8", map[string]string{"ortusnova": "ghs_eight"})
	if st := fh.send(http.MethodDelete, "?job=ONS-7&attempt=1", nil, controlToken); st != http.StatusNoContent {
		t.Fatalf("unregistering: %d", st)
	}
	entries, _ := os.ReadDir(fh.dir)
	if len(entries) != 1 {
		t.Fatalf("the forge directory holds %d attempts, want ONS-8's only", len(entries))
	}
	_ = filepath.WalkDir(fh.dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(path); strings.Contains(string(b), "ghs_seven") || strings.Contains(string(b), "ghs_brian") {
				t.Errorf("%s still holds ONS-7's token", path)
			}
		}
		return nil
	})
	if _, err := net.Dial("tcp", fh.nonos["ONS-7#1"].l.Addr().String()); err == nil {
		t.Error("ONS-7's nono still listens")
	}
	if _, got := fh.get(fh.sortie.mint("ONS-7", Flow), "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova"); got != "Bearer sortie-credential-github-ortusnova" {
		t.Errorf("after the attempt: %q, want the placeholder unreplaced", got)
	}
	if _, got := fh.get(fh.sortie.mint("ONS-8", Flow), "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova"); got != "Bearer ghs_eight" {
		t.Errorf("ONS-8 after ONS-7's end: %q", got)
	}
	// An attempt the gate does not hold is unregistered all the same.
	if st := fh.send(http.MethodDelete, "?job=ONS-7&attempt=1", nil, controlToken); st != http.StatusNoContent {
		t.Errorf("unregistering again: %d, want 204", st)
	}
}

func TestForgeRegistrationsTheGateRefuses(t *testing.T) {
	fh := newForgeHarness(t)
	ok := ForgeRegistration{Job: "ONS-7", Attempt: 1, Tokens: map[string]string{"ortusnova": "ghs_x"}}
	for name, c := range map[string]struct {
		method, query string
		body          any
		token         string
		want          int
	}{
		"no token":               {http.MethodPost, "", ok, "", http.StatusUnauthorized},
		"an egress job token":    {http.MethodPost, "", ok, fh.sortie.mint("ONS-7", Flow), http.StatusUnauthorized},
		"delete, no token":       {http.MethodDelete, "?job=ONS-7&attempt=1", nil, "", http.StatusUnauthorized},
		"no job":                 {http.MethodPost, "", ForgeRegistration{Attempt: 1, Tokens: ok.Tokens}, controlToken, http.StatusBadRequest},
		"no attempt":             {http.MethodPost, "", ForgeRegistration{Job: "ONS-7", Tokens: ok.Tokens}, controlToken, http.StatusBadRequest},
		"no tokens":              {http.MethodPost, "", ForgeRegistration{Job: "ONS-7", Attempt: 1}, controlToken, http.StatusBadRequest},
		"a path as owner":        {http.MethodPost, "", ForgeRegistration{Job: "ONS-7", Attempt: 1, Tokens: map[string]string{"../x": "ghs_x"}}, controlToken, http.StatusBadRequest},
		"an owner twice":         {http.MethodPost, "", ForgeRegistration{Job: "ONS-7", Attempt: 1, Tokens: map[string]string{"OrtusNova": "a", "ortusnova": "b"}}, controlToken, http.StatusBadRequest},
		"a token with a newline": {http.MethodPost, "", ForgeRegistration{Job: "ONS-7", Attempt: 1, Tokens: map[string]string{"ortusnova": "ghs_x\nX: y"}}, controlToken, http.StatusBadRequest},
		"delete, no attempt":     {http.MethodDelete, "?job=ONS-7", nil, controlToken, http.StatusBadRequest},
		"get":                    {http.MethodGet, "", nil, controlToken, http.StatusMethodNotAllowed},
	} {
		if st := fh.send(c.method, c.query, c.body, c.token); st != c.want {
			t.Errorf("%s: %d, want %d", name, st, c.want)
		}
	}
	if entries, _ := os.ReadDir(fh.dir); len(entries) != 0 || fh.starts != 0 {
		t.Errorf("a refused registration left %d attempts and started %d nonos", len(entries), fh.starts)
	}

	// A gate without ForgeTokens takes none.
	h := newHarness(t)
	req, _ := http.NewRequest(http.MethodPost, h.gate.URL+ForgeTokensPath, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+controlToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("a gate without ForgeTokens: %d, want 404", resp.StatusCode)
	}
}

// A gate that restarts takes back its registrations, unless their tokens
// are older than their hour; one whose end the gate was never told is
// swept once its tokens are that old.
func TestRegistrationsOutliveTheGateNotTheirHour(t *testing.T) {
	fh := newForgeHarness(t)
	fh.register("ONS-7", map[string]string{"ortusnova": "ghs_seven"})
	fh.register("ONS-8", map[string]string{"ortusnova": "ghs_eight"})
	eight := fh.g.forge.reg["ONS-8#1"]
	fh.g.forge.close()
	old := time.Now().Add(-forgeIdle - time.Minute)
	if err := os.Chtimes(filepath.Join(eight.dir, "tokens", "ortusnova"), old, old); err != nil {
		t.Fatal(err)
	}

	f, err := newForge(fh.g.forge.cfg, fh.nono.pass, slog.New(slog.NewTextHandler(fh.log, nil)))
	if err != nil {
		t.Fatal(err)
	}
	f.start = fh.start
	f.restore(time.Now())
	fh.g.forge = f
	if _, got := fh.get(fh.sortie.mint("ONS-7", Flow), "https://api.github.com/repos/ortusnova/fleet-vm", "ortusnova"); got != "Bearer ghs_seven" {
		t.Errorf("ONS-7 after the gate's restart: %q, want its token", got)
	}
	if _, ok := f.route("ONS-8", 1, "api.github.com:443"); ok {
		t.Error("ONS-8's registration, older than its hour, was restored")
	}
	if _, err := os.Stat(eight.dir); !os.IsNotExist(err) {
		t.Errorf("ONS-8's directory is still there: %v", err)
	}

	f.sweep(time.Now().Add(forgeIdle + time.Minute))
	if _, ok := f.route("ONS-7", 1, "api.github.com:443"); ok {
		t.Error("the sweep kept ONS-7, whose tokens were not refreshed within their hour")
	}
	if entries, _ := os.ReadDir(fh.dir); len(entries) != 0 {
		t.Errorf("the forge directory holds %d attempts after the sweep", len(entries))
	}
}

func TestCredential(t *testing.T) {
	one := t.TempDir()
	if err := writeTokensFor(one, map[string]string{"ortusnova": "ghs_one"}); err != nil {
		t.Fatal(err)
	}
	two := t.TempDir()
	if err := writeTokensFor(two, map[string]string{"ortusnova": "ghs_o", "briancappello": "ghs_b"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, dir, mode, scope, want string
	}{
		{"its owner", one, "bearer", "ortusnova", "ghs_one"},
		{"the only owner, any path", one, "bearer", "graphql", "ghs_one"},
		{"basic", one, "basic", "ortusnova", "x-access-token:ghs_one"},
		{"two owners", two, "bearer", "briancappello", "ghs_b"},
		{"the owner's case", two, "basic", "OrtusNova", "x-access-token:ghs_o"},
	} {
		var out bytes.Buffer
		if err := Credential(c.dir, c.mode, c.scope, &out); err != nil || out.String() != c.want {
			t.Errorf("%s: %q, %v; want %q", c.name, out.String(), err, c.want)
		}
	}
	for name, c := range map[string][3]string{
		"two owners, neither named": {two, "bearer", "graphql"},
		"a mode":                    {one, "plain", "ortusnova"},
		"no attempt":                {filepath.Join(one, "none"), "bearer", "ortusnova"},
	} {
		var out bytes.Buffer
		if err := Credential(c[0], c[1], c[2], &out); err == nil || out.Len() != 0 {
			t.Errorf("%s: printed %q, err %v; want nothing and an error", name, out.String(), err)
		}
	}
}

func writeTokensFor(dir string, tokens map[string]string) error {
	if err := os.Mkdir(filepath.Join(dir, "tokens"), 0o700); err != nil {
		return err
	}
	return writeTokens(dir, tokens)
}

func TestForgeConfig(t *testing.T) {
	control := writeSecret(t, "control-token", controlToken)
	pass := writeSecret(t, "nono-pass", "pass")
	dir := t.TempDir()
	for name, ft := range map[string]ForgeTokens{
		"no nono":   {CACert: "c", CAKey: "k", Dir: dir},
		"no CA":     {Nono: "nono", CAKey: "k", Dir: dir},
		"no key":    {Nono: "nono", CACert: "c", Dir: dir},
		"no folder": {Nono: "nono", CACert: "c", CAKey: "k"},
	} {
		if _, err := New(Config{Sortie: "http://127.0.0.1:7680", ControlTokenFile: control, Nono: "127.0.0.1:7690", NonoPassFile: pass, ForgeTokens: &ft}, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	d := filepath.Join(dir, "forge")
	if _, err := New(Config{Sortie: "http://127.0.0.1:7680", ControlTokenFile: control, Nono: "127.0.0.1:7690", NonoPassFile: pass,
		ForgeTokens: &ForgeTokens{Nono: "nono", CACert: "c", CAKey: "k", Dir: d}}, slog.New(slog.DiscardHandler)); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(d); err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("the forge directory: %v, %v; want mode 0700", info, err)
	}
}

// The profile's hosts are GitHub's, and its captures run the gate's
// command on the attempt's directory for every request.
func TestForgeProfile(t *testing.T) {
	var p struct {
		Network struct {
			AllowDomain       []string                     `json:"allow_domain"`
			CustomCredentials map[string]map[string]string `json:"custom_credentials"`
		} `json:"network"`
		Capture map[string]struct {
			Command []string `json:"command"`
			TTL     *int     `json:"cache_ttl_secs"`
			Regex   string   `json:"cache_path_regex"`
		} `json:"credential_capture"`
	}
	if err := json.Unmarshal(forgeProfile(github, []string{"/bin/cp-egress", CredentialCommand}, "/run/forge/a-1"), &p); err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Network.AllowDomain, " ") != "api.github.com github.com" {
		t.Errorf("allow_domain = %v", p.Network.AllowDomain)
	}
	api, git := p.Network.CustomCredentials["github_api"], p.Network.CustomCredentials["github_git"]
	if api["upstream"] != "https://api.github.com" || api["credential_key"] != "cmd://github_api" || api["credential_format"] != "Bearer {}" {
		t.Errorf("github_api = %v", api)
	}
	if git["upstream"] != "https://github.com" || git["credential_key"] != "cmd://github_git" || git["inject_mode"] != "basic_auth" {
		t.Errorf("github_git = %v", git)
	}
	for route, mode := range map[string]string{"github_api": "bearer", "github_git": "basic"} {
		c := p.Capture[route]
		if strings.Join(c.Command, " ") != "/bin/cp-egress forge-credential /run/forge/a-1 "+mode || c.TTL == nil || *c.TTL != 0 {
			t.Errorf("%s capture = %+v", route, c)
		}
	}
	for path, want := range map[string]string{"/repos/ortusnova/fleet-vm/pulls": "ortusnova", "/orgs/ortusnova/repos": "ortusnova", "/users/x": "users"} {
		if m := regexp.MustCompile(p.Capture["github_api"].Regex).FindStringSubmatch(path); m == nil || m[1] != want {
			t.Errorf("API scope of %s = %v, want %s", path, m, want)
		}
	}
	if m := regexp.MustCompile(p.Capture["github_git"].Regex).FindStringSubmatch("/briancappello/site.git/info/refs"); m == nil || m[1] != "briancappello" {
		t.Errorf("git scope = %v", m)
	}
}
