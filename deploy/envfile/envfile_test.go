package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const environments = "../environments"

const valid = `version: 1
hosts:
  alpha: {runs: [control, worker]}
  beta:  {runs: [worker]}
control:
  front_port: 9443
ports:
  sortie: 9680
  workbench: 9793
  nono: 9690
  nono_live_proof: 9691
  egress_gate: 9692
  previews_from: 9800
tracker:
  url: http://localhost:9087
  workspace: test
  project: TST
local_names:
  workbench: workbench.test-a.example
`

// throwaway writes files into a temporary directory beside a copy of the
// kit's schema, so no test depends on dev.yml's hosts.
func throwaway(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	schema, err := os.ReadFile(filepath.Join(environments, SchemaFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, SchemaFile), schema, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func values(t *testing.T, args ...string) map[string]string {
	t.Helper()
	var out bytes.Buffer
	if err := run(append([]string{"values"}, args...), &out); err != nil {
		t.Fatal(err)
	}
	// Read the assignments back through a shell, as the kit's scripts do.
	script := out.String() + "\nset | grep '^ORTUS_NOVA_ENV'"
	got, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatalf("bash refused the assignments: %v\n%s", err, out.String())
	}
	vars := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(got)), "\n") {
		k, v, _ := strings.Cut(line, "=")
		vars[k] = strings.Trim(v, "'")
	}
	return vars
}

func TestDev(t *testing.T) {
	v := values(t, "-host", "p1", filepath.Join(environments, "dev.yml"))
	want := map[string]string{
		"ORTUS_NOVA_ENV":                "dev",
		"ORTUS_NOVA_ENV_HOSTS":          "p1 taichi",
		"ORTUS_NOVA_ENV_CONTROL_HOST":   "p1",
		"ORTUS_NOVA_ENV_WORKER_HOSTS":   "p1 taichi",
		"ORTUS_NOVA_ENV_HOST_RUNS":      "control worker",
		"ORTUS_NOVA_ENV_SORTIE_PORT":    "7680",
		"ORTUS_NOVA_ENV_WORKBENCH_PORT": "8793",
		"ORTUS_NOVA_ENV_FRONT_PORT":     "8443",
	}
	for k, w := range want {
		if v[k] != w {
			t.Errorf("%s = %q, want %q", k, v[k], w)
		}
	}
	if v := values(t, "-host", "taichi", filepath.Join(environments, "dev.yml")); v["ORTUS_NOVA_ENV_HOST_RUNS"] != "worker" {
		t.Errorf("taichi runs %q, want worker", v["ORTUS_NOVA_ENV_HOST_RUNS"])
	}
}

// The kit's own directory: every file valid, no port or local name shared.
func TestKitEnvironments(t *testing.T) {
	if _, err := CheckDir(environments); err != nil {
		t.Fatal(err)
	}
}

func TestValues(t *testing.T) {
	dir := throwaway(t, map[string]string{"test-a.yml": valid})
	v := values(t, filepath.Join(dir, "test-a.yml"))
	if v["ORTUS_NOVA_ENV"] != "test-a" || v["ORTUS_NOVA_ENV_TRACKER_URL"] != "http://localhost:9087" ||
		v["ORTUS_NOVA_ENV_WORKBENCH_LOCAL_NAME"] != "workbench.test-a.example" || v["ORTUS_NOVA_ENV_PREVIEW_FIRST_PORT"] != "9800" {
		t.Errorf("values: %v", v)
	}
	if _, ok := v["ORTUS_NOVA_ENV_HOST_RUNS"]; ok {
		t.Error("no -host, yet the host's runs were printed")
	}
	noName := strings.Replace(valid, "local_names:\n  workbench: workbench.test-a.example\n", "", 1)
	dir = throwaway(t, map[string]string{"test-a.yml": noName})
	if v := values(t, filepath.Join(dir, "test-a.yml")); v["ORTUS_NOVA_ENV_WORKBENCH_LOCAL_NAME"] != "" {
		t.Errorf("no local name, yet %q", v["ORTUS_NOVA_ENV_WORKBENCH_LOCAL_NAME"])
	}
}

func TestUndeclaredHost(t *testing.T) {
	dir := throwaway(t, map[string]string{"test-a.yml": valid})
	err := run([]string{"values", "-host", "mini", filepath.Join(dir, "test-a.yml")}, &bytes.Buffer{})
	if err == nil {
		t.Fatal("an undeclared host was accepted")
	}
	for _, want := range []string{"mini", "test-a", "alpha beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal %q does not name %q", err, want)
		}
	}
}

// Each refusal names the invalid entry by its path in the file, and every
// value it refuses.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name, from, to string
		want           []string
	}{
		{"two control hosts", "beta:  {runs: [worker]}", "beta:  {runs: [control]}", []string{"/hosts", "alpha", "beta"}},
		{"no control host", "alpha: {runs: [control, worker]}", "alpha: {runs: [worker]}", []string{"/hosts", "control"}},
		{"an unknown runs value", "beta:  {runs: [worker]}", "beta:  {runs: [builder]}", []string{"/hosts/beta/runs/0"}},
		{"empty runs", "beta:  {runs: [worker]}", "beta:  {runs: []}", []string{"/hosts/beta/runs"}},
		{"a key that is not a DNS label", "beta:  {runs: [worker]}", "Beta_1:  {runs: [worker]}", []string{"/hosts/Beta_1: "}},
		{"a missing port", "  nono: 9690\n", "", []string{"/ports/nono"}},
		{"a port out of range", "sortie: 9680", "sortie: 80", []string{"/ports/sortie"}},
		{"a port used twice", "workbench: 9793", "workbench: 9680", []string{"/ports/workbench", "9680", "/ports/sortie"}},
		{"an unknown key", "version: 1\n", "version: 1\npins: {}\n", []string{"/pins"}},
		{"an unknown version", "version: 1\n", "version: 2\n", []string{"/version"}},
		{"a missing tracker", "tracker:\n  url: http://localhost:9087\n  workspace: test\n  project: TST\n", "", []string{"/tracker"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := strings.Replace(valid, c.from, c.to, 1)
			if body == valid {
				t.Fatalf("the case changed nothing: %q", c.from)
			}
			dir := throwaway(t, map[string]string{"test-a.yml": body})
			_, err := Load(filepath.Join(dir, "test-a.yml"))
			if err == nil {
				t.Fatal("accepted")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal does not name %q:\n%s", w, err)
				}
			}
		})
	}
}

func TestEnvironmentName(t *testing.T) {
	dir := throwaway(t, map[string]string{"Test_A.yml": valid, "test-a.yaml": valid})
	for _, f := range []string{"Test_A.yml", "test-a.yaml"} {
		if _, err := Load(filepath.Join(dir, f)); err == nil || !strings.Contains(err.Error(), f) {
			t.Errorf("%s: want a refusal naming the file, got %v", f, err)
		}
	}
}

func TestSharedPort(t *testing.T) {
	b := strings.NewReplacer("9", "6", "test-a", "test-b").Replace(valid)
	b = strings.Replace(b, "sortie: 6680", "sortie: 9680", 1)
	dir := throwaway(t, map[string]string{"test-a.yml": valid, "test-b.yml": b})
	_, err := CheckDir(dir)
	if err == nil {
		t.Fatal("two files that share a port were accepted")
	}
	for _, w := range []string{"9680", "test-a.yml", "test-b.yml"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not name %q:\n%s", w, err)
		}
	}
}

func TestSharedLocalName(t *testing.T) {
	b := strings.NewReplacer("9", "6").Replace(valid)
	dir := throwaway(t, map[string]string{"test-a.yml": valid, "test-b.yml": b})
	_, err := CheckDir(dir)
	if err == nil {
		t.Fatal("two files that share a local name were accepted")
	}
	for _, w := range []string{"workbench.test-a.example", "test-a.yml", "test-b.yml"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the refusal does not name %q:\n%s", w, err)
		}
	}
}

func TestStage0NamesOnce(t *testing.T) {
	a := strings.Replace(valid, "version: 1\n", "version: 1\nstage0_names: true\n", 1)
	b := strings.NewReplacer("9", "6", "test-a", "test-b").Replace(a)
	dir := throwaway(t, map[string]string{"test-a.yml": a, "test-b.yml": b})
	_, err := CheckDir(dir)
	if err == nil || !strings.Contains(err.Error(), "test-a.yml") || !strings.Contains(err.Error(), "test-b.yml") {
		t.Fatalf("two files with stage0_names: %v", err)
	}
	dir = throwaway(t, map[string]string{"test-a.yml": a})
	if v := values(t, filepath.Join(dir, "test-a.yml")); v["ORTUS_NOVA_ENV_STAGE0_NAMES"] != "true" {
		t.Errorf("stage0_names: %q", v["ORTUS_NOVA_ENV_STAGE0_NAMES"])
	}
}

func TestCoexisting(t *testing.T) {
	b := strings.NewReplacer("9", "6", "test-a", "test-b").Replace(valid)
	dir := throwaway(t, map[string]string{"test-a.yml": valid, "test-b.yml": b})
	envs, err := CheckDir(dir)
	if err != nil || len(envs) != 2 {
		t.Fatalf("two files that share nothing: %v (%d loaded)", err, len(envs))
	}
}

func TestShellQuote(t *testing.T) {
	for _, s := range []string{"", "a b", "it's", "$(touch /nonexistent)", "`x`", "a\nb"} {
		out, err := exec.Command("bash", "-c", "v="+shellQuote(s)+"; printf %s \"$v\"").Output()
		if err != nil || string(out) != s {
			t.Errorf("%q came back as %q (%v)", s, out, err)
		}
	}
}
