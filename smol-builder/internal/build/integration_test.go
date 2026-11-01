package build

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/runner"
	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/stack"
	"github.com/ortusnova/agentic-orchestration/smol-builder/layers"
)

// testMachineLabel marks the machine the integration test creates from the
// pack. Builder machines carry BuilderLabel instead.
const testMachineLabel = "smol-builder-test=1"

// logWriter sends diagnostics to the test log.
type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// failingPack runs real programs but fails smolvm pack create, after the
// builder machine exists. It records the builder's name.
type failingPack struct {
	runner.Runner
	mu      sync.Mutex
	machine string
}

func (f *failingPack) Run(ctx context.Context, name string, args ...string) error {
	if name == "smolvm" && len(args) > 3 && args[0] == "machine" && args[1] == "create" {
		f.mu.Lock()
		f.machine = flagValue(args, "--name")
		f.mu.Unlock()
	}
	if name == "smolvm" && len(args) > 1 && args[0] == "pack" && args[1] == "create" {
		return &runner.Error{Command: "smolvm pack create", ExitCode: 1, Err: os.ErrInvalid}
	}
	return f.Runner.Run(ctx, name, args...)
}

type machineInfo struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	State  string            `json:"state"`
}

func listMachines(t *testing.T) []machineInfo {
	t.Helper()
	out, err := exec.Command("smolvm", "machine", "ls", "--json").Output()
	if err != nil {
		t.Fatalf("smolvm machine ls: %v", err)
	}
	var ms []machineInfo
	if err := json.Unmarshal(out, &ms); err != nil {
		t.Fatalf("smolvm machine ls --json: %v", err)
	}
	return ms
}

// assertNoBuilders fails when a machine named or labelled as a builder exists.
func assertNoBuilders(t *testing.T) {
	t.Helper()
	key, value, _ := strings.Cut(BuilderLabel, "=")
	for _, m := range listMachines(t) {
		if strings.HasPrefix(m.Name, BuilderPrefix) || m.Labels[key] == value {
			t.Errorf("builder machine left behind: %s (%s)", m.Name, m.State)
		}
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// pinnedArg returns the default of ARG name in the shipped layer's Dockerfile.
func pinnedArg(t *testing.T, layer, name string) string {
	t.Helper()
	data, err := fs.ReadFile(layers.FS, layer+"/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(data)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ARG "+name+"="); ok {
			return v
		}
	}
	t.Fatalf("layer %s has no ARG %s", layer, name)
	return ""
}

func smolvm(t *testing.T, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, "smolvm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("smolvm %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func TestIntegrationBuildGoPack(t *testing.T) {
	if os.Getenv("SMOL_BUILDER_SMOLVM_TEST") != "1" {
		t.Skip("set SMOL_BUILDER_SMOLVM_TEST=1 to build a real pack with podman and smolvm")
	}
	assertNoBuilders(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{stack.FileName: "layers = [\"go\"]\n"})
	outDir := t.TempDir()
	output := filepath.Join(outDir, "app.smolmachine")

	b := New(logWriter{t})
	report, err := b.Build(ctx, Options{Repo: repo, Output: output})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	js, _ := json.Marshal(report)
	t.Logf("report: %s", js)
	if !reflect.DeepEqual(report.Layers, []string{"base", "go"}) {
		t.Errorf("layers = %v", report.Layers)
	}
	if got := fileSHA256(t, output); report.PackSHA256 != got {
		t.Errorf("pack_sha256 %s, file sha256 %s", report.PackSHA256, got)
	}
	if info, err := os.Stat(output); err != nil || info.Size() != report.PackBytes {
		t.Errorf("pack_bytes %d, file %v %v", report.PackBytes, info, err)
	}
	if entries, _ := os.ReadDir(outDir); len(entries) != 1 {
		t.Errorf("output directory holds %d entries, want only the pack", len(entries))
	}
	assertNoBuilders(t)

	t.Run("machine from pack", func(t *testing.T) {
		suffix, err := randomHex(4)
		if err != nil {
			t.Fatal(err)
		}
		name := "sbtest-" + suffix
		t.Cleanup(func() {
			out, err := exec.Command("smolvm", "machine", "delete", "--name", name, "-f").CombinedOutput()
			if err != nil {
				t.Errorf("deleting %s: %v\n%s", name, err, out)
			}
		})
		smolvm(t, "machine", "create", "--name", name, "--from", output, "--label", testMachineLabel)
		smolvm(t, "machine", "start", "--name", name)
		out := smolvm(t, "machine", "exec", "--name", name, "--",
			"sh", "-c", `command -v go && go version && echo "layers=$SORTIE_STACK_LAYERS" && pwd`)
		t.Logf("exec output:\n%s", out)
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) < 4 {
			t.Fatalf("exec printed %d lines", len(lines))
		}
		if lines[0] != "/usr/local/go/bin/go" {
			t.Errorf("command -v go = %q", lines[0])
		}
		if want := "go version go" + pinnedArg(t, "go", "GO_VERSION") + " "; !strings.HasPrefix(lines[1], want) {
			t.Errorf("go version = %q", lines[1])
		}
		if lines[2] != "layers=base+go" {
			t.Errorf("SORTIE_STACK_LAYERS line = %q", lines[2])
		}
		if lines[len(lines)-1] != "/workspace" {
			t.Errorf("pwd = %q, want /workspace", lines[len(lines)-1])
		}
	})

	before := fileSHA256(t, output)

	t.Run("failing project layer keeps output", func(t *testing.T) {
		writeFiles(t, repo, map[string]string{
			stack.FileName:        "layers = [\"go\"]\nproject_layer = \"ci/agent.Dockerfile\"\n",
			"ci/agent.Dockerfile": "ARG BASE\nFROM ${BASE}\nRUN false\n",
		})
		_, err := b.Build(ctx, Options{Repo: repo, Output: output})
		if err == nil || !strings.Contains(err.Error(), "layer project") {
			t.Fatalf("err = %v, want one naming layer project", err)
		}
		t.Logf("expected failure: %v", err)
		if got := fileSHA256(t, output); got != before {
			t.Errorf("output changed: %s -> %s", before, got)
		}
		assertNoBuilders(t)
	})

	t.Run("failing pack keeps output and deletes builder", func(t *testing.T) {
		writeFiles(t, repo, map[string]string{stack.FileName: "layers = [\"go\"]\n"})
		fp := &failingPack{Runner: b.Run}
		failing := *b
		failing.Run = fp
		_, err := failing.Build(ctx, Options{Repo: repo, Output: output})
		if err == nil || !strings.Contains(err.Error(), "pack") {
			t.Fatalf("err = %v, want a pack failure", err)
		}
		if fp.machine == "" {
			t.Fatal("the builder machine was never created")
		}
		t.Logf("expected failure after creating %s: %v", fp.machine, err)
		if got := fileSHA256(t, output); got != before {
			t.Errorf("output changed: %s -> %s", before, got)
		}
		if entries, _ := os.ReadDir(outDir); len(entries) != 1 {
			t.Errorf("output directory holds %d entries, want only the pack", len(entries))
		}
		for _, m := range listMachines(t) {
			if m.Name == fp.machine {
				t.Errorf("builder %s still exists", m.Name)
			}
		}
		assertNoBuilders(t)
	})
}
