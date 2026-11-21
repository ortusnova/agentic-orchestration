package egress

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ForgeTokensPath is where Sortie registers an attempt's GitHub tokens:
// POST with a JSON [ForgeRegistration] registers them, replacing what
// the gate held for that attempt; DELETE with the query job and attempt
// forgets them. Both take Sortie's control token as a bearer token.
const ForgeTokensPath = "/v1/forge-tokens" // #nosec G101 -- a path, not a credential

// CredentialCommand is cp-egress's subcommand that an attempt's nono
// runs for each GitHub request: cp-egress forge-credential DIR MODE.
const CredentialCommand = "forge-credential" // #nosec G101 -- a command name, not a credential

// ForgeTokens is the GitHub egress of attempts that hold minted tokens
// (spec store job-scoped-github-tokens, design D6). While an attempt is
// registered, its CONNECTs for github.com and api.github.com go to a
// nono the gate runs for it. That nono's credentials are cmd://: for
// each request it runs [CredentialCommand], which prints the token of the
// owner the request path names from the attempt's directory, so a
// refreshed token reaches the next request. Every other host goes to
// the shared nono, as before.
type ForgeTokens struct {
	// Nono is the nono binary.
	Nono string
	// CACert and CAKey are the CA the attempt's nono intercepts TLS
	// with: the shared nono's, which every job machine trusts.
	CACert, CAKey string
	// Dir holds one directory per registered attempt, with its token
	// files, mode 0700. It should be on a tmpfs.
	Dir string
	// Capture is the command nono runs to read a token, before its two
	// arguments (the attempt's directory and the mode); empty, this
	// executable's [CredentialCommand].
	Capture []string
}

// ForgeRegistration is Sortie's registration of an attempt's tokens.
type ForgeRegistration struct {
	Job     string            `json:"job"`
	Attempt uint64            `json:"attempt"`
	Tokens  map[string]string `json:"tokens"`
}

// forgeUpstreams are the hosts an attempt's nono serves, as nono's
// upstream URLs: GitHub's API, in header mode, and git over HTTPS, in
// basic-auth mode.
type forgeUpstreams struct{ API, Git string }

var github = forgeUpstreams{API: "https://api.github.com", Git: "https://github.com"}

// The cache scopes nono derives from a request's path: the owner of
// /repos/<owner>/..., /orgs/<owner>/... or /<owner>/... on the API, and
// of /<owner>/<repository>.git/... on git.
const (
	apiScope = `^/(?:repos/|orgs/)?([^/]+)`
	gitScope = `^/([^/]+)/`
)

// forgeIdle is how long a registration lives after its tokens were last
// written: Sortie rewrites them 10 minutes before each hour-long token
// expires, so a registration this old belongs to an attempt whose end
// the gate was never told, and its tokens are dead.
const forgeIdle = 70 * time.Minute

var (
	ownerName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	tokenText = regexp.MustCompile(`^[\x21-\x7e]+$`)
)

// attemptNono is a registered attempt and the nono that serves it.
type attemptNono struct {
	key  string
	job  string
	att  uint64
	dir  string
	addr string
	stop func()
}

// forge is the gate's registry of attempts that hold tokens.
type forge struct {
	cfg       ForgeTokens
	upstreams forgeUpstreams
	pass      string
	log       *slog.Logger
	// start runs an attempt's nono on its profile and returns its
	// address and how to stop it; tests replace it.
	start func(a *attemptNono, profile string) (addr string, stop func(), err error)

	ops sync.Mutex // one registration or unregistration at a time
	mu  sync.RWMutex
	reg map[string]*attemptNono
}

func newForge(cfg ForgeTokens, pass string, log *slog.Logger) (*forge, error) {
	if cfg.Nono == "" || cfg.CACert == "" || cfg.CAKey == "" || cfg.Dir == "" {
		return nil, errors.New("forge tokens: want the nono binary, the CA's certificate and key, and the attempts' directory")
	}
	if len(cfg.Capture) == 0 {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("forge tokens: the capture command: %w", err)
		}
		cfg.Capture = []string{exe, CredentialCommand}
	}
	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("forge tokens: %w", err)
	}
	if err := os.Chmod(cfg.Dir, 0o700); err != nil { // #nosec G302 -- a directory: its owner needs to enter it
		return nil, fmt.Errorf("forge tokens: %w", err)
	}
	f := &forge{cfg: cfg, upstreams: github, pass: pass, log: log, reg: map[string]*attemptNono{}}
	f.start = f.startNono
	return f, nil
}

func attemptKey(job string, attempt uint64) string {
	return job + "#" + strconv.FormatUint(attempt, 10)
}

// hostport is the CONNECT target of an upstream URL.
func hostport(upstream string) string {
	u, err := url.Parse(upstream)
	if err != nil {
		return ""
	}
	if u.Port() != "" {
		return strings.ToLower(u.Host)
	}
	return strings.ToLower(u.Hostname()) + ":443"
}

// route returns the address of the nono that serves job's attempt on
// host, when the attempt is registered and host is one of GitHub's.
func (f *forge) route(job string, attempt uint64, host string) (string, bool) {
	if f == nil {
		return "", false
	}
	h := strings.ToLower(host)
	if h != hostport(f.upstreams.API) && h != hostport(f.upstreams.Git) {
		return "", false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	a, ok := f.reg[attemptKey(job, attempt)]
	if !ok {
		return "", false
	}
	return a.addr, true
}

// serveForge answers a registration or an unregistration.
func (g *Gate) serveForge(w http.ResponseWriter, r *http.Request) {
	if g.forge == nil {
		http.Error(w, "this egress gate takes no GitHub tokens", http.StatusNotFound)
		return
	}
	if t := bearer(r.Header.Get("Authorization")); t == "" || subtle.ConstantTimeCompare([]byte(t), []byte(g.controlToken)) != 1 {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sortie-control"`)
		http.Error(w, "the control token is required", http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodPost:
		var reg ForgeRegistration
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&reg); err != nil {
			http.Error(w, "the registration is not JSON", http.StatusBadRequest)
			return
		}
		if err := reg.validate(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := g.forge.register(reg); err != nil {
			g.log.Warn("GitHub tokens not registered", slog.String("job", reg.Job), slog.Uint64("attempt", reg.Attempt), slog.String("error", err.Error()))
			http.Error(w, "the attempt's GitHub egress could not start", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		job := r.URL.Query().Get("job")
		attempt, err := strconv.ParseUint(r.URL.Query().Get("attempt"), 10, 64)
		if job == "" || err != nil || attempt == 0 {
			http.Error(w, "want the query job and attempt", http.StatusBadRequest)
			return
		}
		g.forge.unregister(job, attempt, "Sortie ended the attempt")
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "POST or DELETE", http.StatusMethodNotAllowed)
	}
}

func (reg ForgeRegistration) validate() error {
	if strings.TrimSpace(reg.Job) == "" || reg.Attempt == 0 {
		return errors.New("the registration needs its job and attempt")
	}
	if len(reg.Tokens) == 0 {
		return errors.New("the registration holds no token")
	}
	seen := map[string]bool{}
	for owner, tok := range reg.Tokens {
		if !ownerName.MatchString(owner) {
			return fmt.Errorf("%q is not a GitHub account name", owner)
		}
		if seen[strings.ToLower(owner)] {
			return fmt.Errorf("the owner %q is named twice", owner)
		}
		seen[strings.ToLower(owner)] = true
		if !tokenText.MatchString(tok) {
			return fmt.Errorf("the token of %q is empty or holds a space or a control character", owner)
		}
	}
	return nil
}

func ownersOf(tokens map[string]string) string {
	owners := make([]string, 0, len(tokens))
	for o := range tokens {
		owners = append(owners, strings.ToLower(o))
	}
	slices.Sort(owners)
	return strings.Join(owners, ",")
}

// register writes the attempt's tokens and starts its nono, or, for an
// attempt already registered, rewrites its tokens, which its nono reads
// for the next request.
func (f *forge) register(reg ForgeRegistration) error {
	f.ops.Lock()
	defer f.ops.Unlock()
	key := attemptKey(reg.Job, reg.Attempt)
	f.mu.RLock()
	a, ok := f.reg[key]
	f.mu.RUnlock()
	if ok {
		if err := writeTokens(a.dir, reg.Tokens); err != nil {
			return err
		}
		f.log.Info("GitHub tokens refreshed", slog.String("job", reg.Job), slog.Uint64("attempt", reg.Attempt), slog.String("owners", ownersOf(reg.Tokens)))
		return nil
	}
	sum := sha256.Sum256([]byte(key))
	a = &attemptNono{key: key, job: reg.Job, att: reg.Attempt, dir: filepath.Join(f.cfg.Dir, "a-"+hex.EncodeToString(sum[:12]))}
	if err := f.prepare(a, reg.Tokens); err != nil {
		_ = os.RemoveAll(a.dir)
		return err
	}
	if err := f.launch(a); err != nil {
		_ = os.RemoveAll(a.dir)
		return err
	}
	f.log.Info("GitHub tokens registered", slog.String("job", reg.Job), slog.Uint64("attempt", reg.Attempt), slog.String("owners", ownersOf(reg.Tokens)), slog.String("nono", a.addr))
	return nil
}

// prepare makes the attempt's directory: its job, its token files and
// the directories its nono keeps its state in.
func (f *forge) prepare(a *attemptNono, tokens map[string]string) error {
	if err := os.RemoveAll(a.dir); err != nil {
		return err
	}
	for _, d := range []string{a.dir, filepath.Join(a.dir, "tokens"), filepath.Join(a.dir, "home")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			return err
		}
	}
	meta, err := json.Marshal(map[string]any{"job": a.job, "attempt": a.att})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(a.dir, "attempt.json"), meta, 0o600); err != nil {
		return err
	}
	return writeTokens(a.dir, tokens)
}

// writeTokens replaces the attempt's token files, one per owner, each
// by a rename, so a capture never reads half a token; an owner no
// longer named loses its file.
func writeTokens(dir string, tokens map[string]string) error {
	tdir := filepath.Join(dir, "tokens")
	keep := map[string]bool{}
	for owner, tok := range tokens {
		name := strings.ToLower(owner)
		keep[name] = true
		tmp := filepath.Join(tdir, "."+name+".new")
		if err := os.WriteFile(tmp, []byte(tok), 0o600); err != nil {
			return err
		}
		if err := os.Rename(tmp, filepath.Join(tdir, name)); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(tdir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !keep[e.Name()] {
			if err := os.Remove(filepath.Join(tdir, e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

// launch writes the attempt's nono profile, starts its nono and adds
// the attempt to the routes.
func (f *forge) launch(a *attemptNono) error {
	profile := filepath.Join(a.dir, "profile.json")
	if err := os.WriteFile(profile, forgeProfile(f.upstreams, f.cfg.Capture, a.dir), 0o600); err != nil {
		return err
	}
	addr, stop, err := f.start(a, profile)
	if err != nil {
		return err
	}
	a.addr, a.stop = addr, stop
	f.mu.Lock()
	f.reg[a.key] = a
	f.mu.Unlock()
	return nil
}

// unregister stops the attempt's nono and deletes its directory: its
// tokens, its profile and what nono kept there.
func (f *forge) unregister(job string, attempt uint64, why string) {
	f.ops.Lock()
	defer f.ops.Unlock()
	f.mu.Lock()
	a, ok := f.reg[attemptKey(job, attempt)]
	delete(f.reg, attemptKey(job, attempt))
	f.mu.Unlock()
	if !ok {
		return
	}
	f.drop(a, why)
}

func (f *forge) drop(a *attemptNono, why string) {
	if a.stop != nil {
		a.stop()
	}
	if err := os.RemoveAll(a.dir); err != nil {
		f.log.Warn("GitHub tokens of an ended attempt not deleted", slog.String("job", a.job), slog.Uint64("attempt", a.att), slog.String("error", err.Error()))
	}
	f.log.Info("GitHub tokens unregistered", slog.String("job", a.job), slog.Uint64("attempt", a.att), slog.String("reason", why))
}

// restore takes back the registrations a previous gate left in Dir, so a
// restart of the gate alone keeps each attempt's GitHub egress; one whose
// tokens are older than forgeIdle, or that cannot start, is deleted.
func (f *forge) restore(now time.Time) {
	entries, err := os.ReadDir(f.cfg.Dir)
	if err != nil {
		f.log.Warn("GitHub token registrations not read", slog.String("error", err.Error()))
		return
	}
	for _, e := range entries {
		dir := filepath.Join(f.cfg.Dir, e.Name())
		a, err := readAttempt(dir)
		if err == nil && now.Sub(tokensWritten(dir)) > forgeIdle {
			err = errors.New("its tokens are older than their hour")
		}
		if err == nil {
			err = f.launch(a)
		}
		if err != nil {
			_ = os.RemoveAll(dir)
			f.log.Warn("GitHub token registration of a previous gate deleted", slog.String("dir", e.Name()), slog.String("error", err.Error()))
			continue
		}
		f.log.Info("GitHub tokens restored", slog.String("job", a.job), slog.Uint64("attempt", a.att), slog.String("nono", a.addr))
	}
}

func readAttempt(dir string) (*attemptNono, error) {
	b, err := os.ReadFile(filepath.Join(dir, "attempt.json")) // #nosec G304 -- the gate's own directory
	if err != nil {
		return nil, err
	}
	var meta struct {
		Job     string `json:"job"`
		Attempt uint64 `json:"attempt"`
	}
	if err := json.Unmarshal(b, &meta); err != nil || meta.Job == "" || meta.Attempt == 0 {
		return nil, errors.New("attempt.json does not name the attempt")
	}
	return &attemptNono{key: attemptKey(meta.Job, meta.Attempt), job: meta.Job, att: meta.Attempt, dir: dir}, nil
}

// tokensWritten is when the newest token file of dir was written.
func tokensWritten(dir string) time.Time {
	var newest time.Time
	entries, _ := os.ReadDir(filepath.Join(dir, "tokens"))
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// sweep unregisters each attempt whose tokens were not written for
// forgeIdle: Sortie's unregistration was lost, and its tokens are dead.
func (f *forge) sweep(now time.Time) {
	f.mu.RLock()
	var stale []*attemptNono
	for _, a := range f.reg {
		if now.Sub(tokensWritten(a.dir)) > forgeIdle {
			stale = append(stale, a)
		}
	}
	f.mu.RUnlock()
	for _, a := range stale {
		f.unregister(a.job, a.att, "its tokens were not refreshed within their hour")
	}
}

// close stops every attempt's nono and keeps their directories, which
// the next gate restores.
func (f *forge) close() {
	f.ops.Lock()
	defer f.ops.Unlock()
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, a := range f.reg {
		if a.stop != nil {
			a.stop()
		}
		delete(f.reg, k)
	}
}

// forgeProfile is the nono profile of an attempt whose directory is dir.
func forgeProfile(up forgeUpstreams, capture []string, dir string) []byte {
	cmd := func(mode string) []string { return append(slices.Clone(capture), dir, mode) }
	host := func(upstream string) string {
		h, _, _ := net.SplitHostPort(hostport(upstream))
		return h
	}
	p := map[string]any{
		"meta": map[string]any{
			"name":        "ortus-forge-attempt",
			"description": "An attempt's GitHub egress (cp-egress, ForgeTokens): its tokens, by the owner the request path names",
		},
		"network": map[string]any{
			"allow_domain": []string{host(up.API), host(up.Git)},
			"credentials":  []string{"github_api", "github_git"},
			"custom_credentials": map[string]any{
				"github_api": map[string]any{ // #nosec G101 -- nono reads the token from the capture command
					"upstream": up.API, "credential_key": "cmd://github_api", "env_var": "SORTIE_GITHUB_API",
					"inject_header": "Authorization", "credential_format": "Bearer {}",
				},
				"github_git": map[string]any{ // #nosec G101 -- nono reads the token from the capture command
					"upstream": up.Git, "credential_key": "cmd://github_git", "env_var": "SORTIE_GITHUB_GIT",
					"inject_mode": "basic_auth",
				},
			},
		},
		"credential_capture": map[string]any{
			"github_api": map[string]any{"command": cmd("bearer"), "timeout_secs": 5, "cache_ttl_secs": 0, "cache_path_regex": apiScope},
			"github_git": map[string]any{"command": cmd("basic"), "timeout_secs": 5, "cache_ttl_secs": 0, "cache_path_regex": gitScope},
		},
	}
	b, _ := json.MarshalIndent(p, "", "  ")
	return b
}

// startNono runs the attempt's nono on a free loopback port and waits
// until it listens. Its home and state are in the attempt's directory,
// so deleting that directory deletes all of it.
func (f *forge) startNono(a *attemptNono, profile string) (string, func(), error) {
	ctx := context.Background()
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	home := filepath.Join(a.dir, "home")
	cmd := exec.CommandContext(ctx, f.cfg.Nono, "--silent", "proxy", "-v", "--listen", "127.0.0.1", "--port", strconv.Itoa(port), // #nosec G204 -- the configuration names nono
		"--profile", profile, "--proxy-ca-cert", f.cfg.CACert, "--proxy-ca-key", f.cfg.CAKey)
	cmd.Env = []string{
		"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + home,
		"XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"NONO_PROXY_PASS=" + f.pass, "NONO_NO_UPDATE_CHECK=1",
	}
	if v, ok := os.LookupEnv("SSL_CERT_FILE"); ok {
		cmd.Env = append(cmd.Env, "SSL_CERT_FILE="+v)
	}
	cmd.Dir = a.dir
	out, err := cmd.StderrPipe()
	if err != nil {
		return "", nil, err
	}
	if err := cmd.Start(); err != nil {
		return "", nil, err
	}
	done := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			f.log.Info("forge nono", slog.String("job", a.job), slog.Uint64("attempt", a.att), slog.String("line", sc.Text()))
		}
		_ = cmd.Wait()
		close(done)
	}()
	stop := func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(10 * time.Second)
	for {
		c, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = c.Close()
			return addr, stop, nil
		}
		select {
		case <-done:
			return "", nil, fmt.Errorf("nono exited before it listened on %s", addr)
		default:
		}
		if time.Now().After(deadline) {
			stop()
			return "", nil, fmt.Errorf("nono did not listen on %s within 10s", addr)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Credential prints, for nono's capture of an attempt whose directory is
// dir, the token of the owner scope names: the owner the request path
// names, as nono's cache_path_regex derives it. An attempt with one
// owner gets its token whatever the path; an attempt with two gets none
// for a path that names neither. mode basic prints it as git's
// x-access-token:<token>.
func Credential(dir, mode, scope string, out io.Writer) error {
	if mode != "bearer" && mode != "basic" {
		return fmt.Errorf("mode %q: want bearer or basic", mode)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "tokens"))
	if err != nil {
		return err
	}
	var owners []string
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") && e.Type().IsRegular() {
			owners = append(owners, e.Name())
		}
	}
	owner := ""
	switch s := strings.ToLower(scope); {
	case slices.Contains(owners, s):
		owner = s
	case len(owners) == 1:
		owner = owners[0]
	default:
		return fmt.Errorf("the attempt holds no token for the owner %q", scope)
	}
	tok, err := os.ReadFile(filepath.Join(dir, "tokens", owner)) // #nosec G304 -- a name read from the attempt's own directory
	if err != nil {
		return err
	}
	if mode == "basic" {
		_, err = fmt.Fprintf(out, "x-access-token:%s", tok)
		return err
	}
	_, err = out.Write(tok)
	return err
}
