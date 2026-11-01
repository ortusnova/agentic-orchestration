//go:build unix

package build

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/stack"
)

// TestIntegrationSweepsKilledBuild kills a build, with every program it
// started, once its builder machine exists, and checks that the next build
// deletes the machine it left.
func TestIntegrationSweepsKilledBuild(t *testing.T) {
	if os.Getenv("SMOL_BUILDER_SMOLVM_TEST") != "1" {
		t.Skip("set SMOL_BUILDER_SMOLVM_TEST=1 to build real packs with podman and smolvm")
	}
	assertNoBuilders(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	bin := filepath.Join(t.TempDir(), "smol-builder")
	if out, err := exec.CommandContext(ctx, "go", "build", "-o", bin, "../../cmd/smol-builder").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	repo := t.TempDir()
	writeFiles(t, repo, map[string]string{stack.FileName: "layers = []\n"})
	outDir := t.TempDir()
	output := filepath.Join(outDir, "base.smolmachine")

	killed := exec.CommandContext(ctx, bin, "build", "--repo", repo, "--output", output)
	killed.Stderr = logWriter{t}
	killed.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := killed.Start(); err != nil {
		t.Fatal(err)
	}
	pid := strconv.Itoa(killed.Process.Pid)

	var left string
	for deadline := time.Now().Add(10 * time.Minute); left == "" && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		for _, m := range listMachines(t) {
			if m.Labels[LabelPID] == pid {
				left = m.Name
			}
		}
	}
	if left == "" {
		_ = syscall.Kill(-killed.Process.Pid, syscall.SIGKILL)
		_ = killed.Wait()
		t.Fatal("the build never created its builder machine")
	}
	t.Cleanup(func() {
		for _, m := range listMachines(t) {
			if m.Name == left {
				t.Errorf("%s survived the next build", left)
				_ = exec.Command("smolvm", "machine", "delete", "--name", left, "-f").Run()
			}
		}
	})
	if err := syscall.Kill(-killed.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = killed.Wait()
	t.Logf("killed build %s after it created %s", pid, left)

	// The killed build's work directory, which holds its image archive.
	var work string
	dirs, _ := filepath.Glob(filepath.Join(os.TempDir(), "smol-builder-*"))
	for _, d := range dirs {
		if data, err := os.ReadFile(filepath.Join(d, ownerFile)); err == nil && strings.Contains(string(data), `"`+LabelPID+`":"`+pid+`"`) {
			work = d
		}
	}
	if work == "" {
		t.Fatal("the killed build left no work directory to sweep")
	}

	found := false
	for _, m := range listMachines(t) {
		found = found || m.Name == left
	}
	if !found {
		t.Fatalf("%s is gone already; the kill left nothing to sweep", left)
	}

	b := New(logWriter{t})
	if _, err := b.Build(ctx, Options{Repo: repo, Output: output}); err != nil {
		t.Fatalf("next build: %v", err)
	}
	for _, m := range listMachines(t) {
		if m.Name == left {
			t.Fatalf("the next build kept %s", left)
		}
	}
	if _, err := os.Stat(work); !os.IsNotExist(err) {
		t.Errorf("the next build kept the killed build's work directory %s: %v", work, err)
	}
	assertNoBuilders(t)
}
