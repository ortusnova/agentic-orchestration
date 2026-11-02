package machine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSmolvm writes a smolvm stand-in that logs each invocation and
// echoes the standard input of exec -i.
func fakeSmolvm(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	bin = filepath.Join(dir, "smolvm")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$*\" in *' -i '*) cat ;; esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestSmolvmLifecycle(t *testing.T) {
	bin, log := fakeSmolvm(t)
	s := &Smolvm{Binary: bin}
	ctx := context.Background()
	if err := s.Available(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := s.Create(ctx, Spec{
		Name: "wd-ONS-7-1", Pack: "/packs/agent.smolmachine", User: "builder", CPUs: 2, MemMiB: 4096, DiskGiB: 10,
		Labels: map[string]string{"worker-daemon.job": "ONS-7"},
		Share:  "/state/jobs/ONS-7-1/share",
		Forwards: []Forward{
			{Name: "egress", HostSocket: "/state/jobs/ONS-7-1/egress.sock", GuestPort: 47801},
			{Name: "mcp", HostSocket: "/state/jobs/ONS-7-1/mcp.sock", GuestPort: 47802},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := m.GuestAddr("egress"); got != "127.0.0.1:47801" {
		t.Errorf("egress guest address = %s", got)
	}
	var out bytes.Buffer
	code, err := m.Exec(ctx, Exec{Argv: []string{"agent", "acp"}, Env: []string{"A=1"}, Workdir: GuestWorkspace,
		Stdin: strings.NewReader("prompt\n"), Stdout: &out})
	if err != nil || code != 0 {
		t.Fatalf("exec: %d, %v", code, err)
	}
	if out.String() != "prompt\n" {
		t.Errorf("stdin did not reach the command: %q", out.String())
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	calls := strings.Split(strings.TrimSpace(string(b)), "\n")
	want := []string{
		"--version",
		"machine create --error-format json --name wd-ONS-7-1 --from /packs/agent.smolmachine --cpus 2 --mem 4096 --storage 10 --label worker-daemon.job=ONS-7 --user builder --init chown builder /workspace -v /state/jobs/ONS-7-1/share:/run/worker --mount-socket /state/jobs/ONS-7-1/egress.sock:/run/worker-egress.sock --mount-socket /state/jobs/ONS-7-1/mcp.sock:/run/worker-mcp.sock",
		"machine start --error-format json --name wd-ONS-7-1",
		"machine exec --error-format json --name wd-ONS-7-1 --stream -- socat -V",
		"machine exec --error-format json --name wd-ONS-7-1 -d -- socat TCP-LISTEN:47801,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:/run/worker-egress.sock",
		"machine exec --error-format json --name wd-ONS-7-1 -d -- socat TCP-LISTEN:47802,bind=127.0.0.1,reuseaddr,fork UNIX-CONNECT:/run/worker-mcp.sock",
		"machine exec --error-format json --name wd-ONS-7-1 -i -w /workspace -e A=1 -- agent acp",
		"machine stop --error-format json --name wd-ONS-7-1",
		"machine delete --error-format json -f --name wd-ONS-7-1",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
}

// The machine gets no network of its own: no flag that would open one.
func TestCreateArgsOpenNoNetwork(t *testing.T) {
	args, err := CreateArgs(Spec{Name: "m", Pack: "/p.smolmachine", Share: "/s", Forwards: []Forward{{Name: "egress", HostSocket: "/e.sock", GuestPort: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		for _, bad := range []string{"--net", "--allow-host", "--credential", "--ssh-agent", "--secret-env"} {
			if strings.HasPrefix(a, bad) {
				t.Errorf("create carries %s", a)
			}
		}
	}
	if _, err := CreateArgs(Spec{Name: "m", Pack: "/p", Share: "/a:b"}); err == nil {
		t.Error("a path with ':' should be refused")
	}
}

func TestCreateDeletesAMachineThatDoesNotComeUp(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "smolvm")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$*\" in *socat*) exit 1 ;; esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Smolvm{Binary: bin, ReadyTries: 2, ReadyWait: 1}
	if _, err := s.Create(context.Background(), Spec{Name: "m", Pack: "/p"}); err == nil || !strings.Contains(err.Error(), "socat") {
		t.Fatalf("err = %v, want one naming socat", err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "machine delete --error-format json -f --name m") {
		t.Errorf("the machine was not deleted:\n%s", b)
	}
}
