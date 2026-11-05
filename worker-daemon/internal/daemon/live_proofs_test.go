package daemon_test

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/egress"
)

// liveProofs is worker-daemon/live-proofs.json: the proofs a live-proof
// job of the job profile it names runs, each a command run from
// worker-daemon/ in the live-proof pack, which sets LIVE_PROOF_BASE_PACK
// (docs/operator-guide.md, "Live proofs of the smolvm tests").
type liveProofs struct {
	Profile string `json:"profile"`
	Proofs  []struct {
		Name    string `json:"name"`
		Proves  string `json:"proves"`
		Command string `json:"command"`
	} `json:"proofs"`
}

func readLiveProofs(t *testing.T) liveProofs {
	t.Helper()
	b, err := os.ReadFile("../../live-proofs.json")
	if err != nil {
		t.Fatal(err)
	}
	var lp liveProofs
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&lp); err != nil {
		t.Fatalf("live-proofs.json: %v", err)
	}
	return lp
}

// fakeTools writes the tools the declared commands run into a directory
// of their own: go, pasta, unshare, mount, ip and dnsmasq, each of which
// records its arguments in log, go with the end-to-end variables too.
func fakeTools(t *testing.T, log string) string {
	t.Helper()
	dir := t.TempDir()
	tools := map[string]string{
		"go": `echo "go $* | pack=$WORKER_DAEMON_E2E_PACK no_skip=$WORKER_DAEMON_E2E_NO_SKIP allowed=$WORKER_DAEMON_E2E_ALLOWED` +
			` name=$WORKER_DAEMON_E2E_PRIVATE_NAME port=$WORKER_DAEMON_E2E_PRIVATE_PORT" >> "$LOG"`,
		"pasta":   `echo "pasta $*" >> "$LOG"; while [ "$1" != -- ]; do shift; done; shift; exec "$@"`,
		"unshare": `echo "unshare $*" >> "$LOG"; while case $1 in -*) true ;; *) false ;; esac; do shift; done; exec "$@"`,
		"mount":   `echo "mount $*" >> "$LOG"`,
		"ip":      `echo "ip $*" >> "$LOG"`,
		"dnsmasq": `echo "dnsmasq $*" >> "$LOG"; exec sleep 10`,
	}
	for name, body := range tools {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { // #nosec G306 -- a fake tool, run by the test
			t.Fatal(err)
		}
	}
	t.Setenv("LOG", log)
	return dir
}

// runDeclared runs a declared command as the live-proof job does, from
// worker-daemon/ with LIVE_PROOF_BASE_PACK set, with the fake tools first
// on PATH, and returns what they recorded.
func runDeclared(t *testing.T, command string) []string {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("skipped: no sh")
	}
	log := filepath.Join(t.TempDir(), "log")
	tools := fakeTools(t, log)
	cmd := exec.Command("sh", "-c", command) // #nosec G204 -- the repository's own declaration
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "PATH="+tools+string(os.PathListSeparator)+os.Getenv("PATH"),
		"LIVE_PROOF_BASE_PACK=/opt/live-proof/base/base.smolmachine", "LOG="+log)
	for _, k := range []string{"WORKER_DAEMON_E2E_PACK", "WORKER_DAEMON_E2E_NO_SKIP", "WORKER_DAEMON_E2E_PRIVATE_PORT", "PRIVATE_LOCAL"} {
		cmd.Env = append(cmd.Env, k+"=")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", command, err, out)
	}
	b, err := os.ReadFile(log) // #nosec G304 -- the test's own file
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// The repository declares the live proofs of TestSmolvmEndToEnd and
// TestSmolvmEgressEndToEnd where the job profile nested reads them (spec
// store agent-platform/supervisor-autonomy, "A live proof that needs nested
// KVM, smolvm or the host runs in-band"): each runs its test alone, in the
// pack the live-proof pack gives, and fails rather than skips where it
// cannot run; the egress one runs with the operator guide's test resolver
// in a network namespace of its own.
func TestTheLiveProofsAreDeclared(t *testing.T) {
	lp := readLiveProofs(t)
	if lp.Profile != "nested" {
		t.Errorf("live-proofs.json names the profile %q, want nested", lp.Profile)
	}
	var names []string
	for _, p := range lp.Proofs {
		names = append(names, p.Name)
		if p.Proves == "" || p.Command == "" {
			t.Errorf("%s: want what it proves and its command", p.Name)
		}
	}
	if want := []string{"TestSmolvmEndToEnd", "TestSmolvmEgressEndToEnd"}; !slices.Equal(names, want) {
		t.Fatalf("live-proofs.json declares %v, want %v", names, want)
	}
	src, err := os.ReadFile("smolvm_e2e_test.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if !strings.Contains(string(src), "\nfunc "+n+"(t *testing.T) {") {
			t.Errorf("no test %s in smolvm_e2e_test.go", n)
		}
	}
	const env = "pack=/opt/live-proof/base/base.smolmachine no_skip=1"

	got := runDeclared(t, lp.Proofs[0].Command)
	if len(got) != 1 || !strings.HasPrefix(got[0], "go test ") || !strings.Contains(got[0], "-run ^TestSmolvmEndToEnd$ ./internal/daemon/ |") ||
		!strings.Contains(got[0], env) {
		t.Errorf("TestSmolvmEndToEnd's proof ran %q, want go test of it alone with %s", got, env)
	}

	got = runDeclared(t, lp.Proofs[1].Command)
	run := regexp.MustCompile(`private-name-netns\.sh (\S+) (\S+)$`).FindStringSubmatch(lp.Proofs[1].Command)
	if run == nil {
		t.Fatalf("TestSmolvmEgressEndToEnd's proof %q does not run testdata/private-name-netns.sh NAME ADDR", lp.Proofs[1].Command)
	}
	name, addr := run[1], netip.MustParseAddr(run[2])
	if addr.IsLoopback() || !slices.ContainsFunc(egress.DeniedRanges, func(r netip.Prefix) bool { return r.Contains(addr) }) {
		t.Errorf("the private name answers %s, want a private address of design D15 that is not loopback", addr)
	}
	tests := slices.DeleteFunc(slices.Clone(got), func(l string) bool { return !strings.HasPrefix(l, "go test ") })
	if len(tests) != 2 || !strings.Contains(tests[0], "-run ^$ ") {
		t.Fatalf("go ran %q, want a build of the package, then the test", tests)
	}
	if !strings.Contains(tests[1], "-run ^TestSmolvmEgressEndToEnd$ .") || !strings.Contains(tests[1], env) ||
		!strings.Contains(tests[1], "allowed=api.github.com") || !strings.Contains(tests[1], "name="+name+" port=53") {
		t.Errorf("the egress test ran as %q, want it alone with %s, the private name %s and dnsmasq's port", tests[1], env, name)
	}
	for _, want := range []string{
		"pasta --config-net --no-map-gw -- unshare --mount sh -euc",
		"ip address add " + addr.String() + "/32 dev lo",
		"dnsmasq --no-daemon --listen-address=127.0.0.1," + addr.String() + " ",
		"mount --bind ",
	} {
		if !slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, want) }) {
			t.Errorf("the egress proof ran no %q:\n%s", want, strings.Join(got, "\n"))
		}
	}
}

// Without PRIVATE_LOCAL, testdata/private-name-netns.sh is as the
// operator guide runs it on a LAN: ADDR is a host of the network, and the
// namespace takes no address.
func TestPrivateNameNetnsWithAHostOfTheNetwork(t *testing.T) {
	got := runDeclared(t, "internal/daemon/testdata/private-name-netns.sh p1.test 192.168.1.165")
	if slices.ContainsFunc(got, func(l string) bool { return strings.HasPrefix(l, "ip ") }) {
		t.Errorf("the namespace took an address: %q", got)
	}
	if !slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "--listen-address=127.0.0.1 ") }) ||
		!slices.ContainsFunc(got, func(l string) bool { return strings.Contains(l, "name=p1.test port=") && strings.HasSuffix(l, "port=") }) {
		t.Errorf("ran %q, want dnsmasq on 127.0.0.1 alone and the test's own port", got)
	}
}
