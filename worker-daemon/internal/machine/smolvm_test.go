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
// Scenario: Absent. A machine of an offer with no run.egress gets no
// network flag at all.
func TestCreateArgsWithoutEgressHaveNoNetwork(t *testing.T) {
	args, err := CreateArgs(Spec{Name: "m", Pack: "/p.smolmachine", Share: "/s", Forwards: []Forward{{Name: "egress", HostSocket: "/e.sock", GuestPort: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range args {
		for _, bad := range []string{"--net", "--allow-", "--block-private", "--credential", "--ssh-agent", "--secret-env"} {
			if strings.HasPrefix(a, bad) {
				t.Errorf("create carries %s", a)
			}
		}
	}
	if _, err := CreateArgs(Spec{Name: "m", Pack: "/p", Share: "/a:b"}); err == nil {
		t.Error("a path with ':' should be refused")
	}
}

// Scenario: The flags (design D8). A network gives virtio-net with private
// ranges blocked, each host by name, each UDP and private entry, and the
// socket forwards as before; never --allow-host or a bare --net.
func TestCreateArgsOfANetwork(t *testing.T) {
	args, err := CreateArgs(Spec{Name: "m", Pack: "/p.smolmachine", Share: "/s",
		Forwards: []Forward{{Name: "egress", HostSocket: "/e.sock", GuestPort: 1}},
		Network:  &Network{Hosts: []string{"models.opencode.ai", "*.githubusercontent.com"}, UDP: []string{"0.0.0.0/0:123"}, Private: []string{"192.168.122.0/24"}}})
	if err != nil {
		t.Fatal(err)
	}
	line := " " + strings.Join(args, " ") + " "
	for _, want := range []string{
		" --net-backend virtio-net ", " --block-private ",
		" --allow-host-pattern models.opencode.ai ", " --allow-host-pattern *.githubusercontent.com ",
		" --allow-udp 0.0.0.0/0:123 ", " --allow-private 192.168.122.0/24 ",
		" --mount-socket /e.sock:/run/worker-egress.sock ",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("create args lack%s: %s", want, line)
		}
	}
	for i, a := range args {
		switch {
		case a == "--allow-host", a == "--net", strings.HasPrefix(a, "--net="), a == "--allow-cidr", a == "--allow-all":
			t.Errorf("create carries %s: %s", a, line)
		case strings.HasPrefix(a, "--allow-") && a != "--allow-host-pattern" && a != "--allow-udp" && a != "--allow-private":
			t.Errorf("create carries %s: %s", a, line)
		case a == "--net-backend" && args[i+1] != "virtio-net":
			t.Errorf("net backend %s", args[i+1])
		}
	}
	for _, bad := range []*Network{
		{},
		{Hosts: []string{"--net"}},
		{Hosts: []string{"a.example,b.example"}},
		{UDP: []string{""}},
		{UDP: []string{"0.0.0.0/0:123"}},
		{Private: []string{"192.168.122.0/24"}},
	} {
		if _, err := CreateArgs(Spec{Name: "m", Pack: "/p", Network: bad}); err == nil {
			t.Errorf("network %+v should be refused", bad)
		}
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

// An image's inputs reach the machine: nested virtualization, its host
// folders, read-only where configured, and its host environment on every
// smolvm call for the machine.
func TestCreateArgsCarryTheImageInputs(t *testing.T) {
	args, err := CreateArgs(Spec{Name: "m", Pack: "/p.smolmachine", Nested: true, Mounts: []Mount{
		{Host: "/c/sources", Guest: "/home/builder/.cache/homelab-sources", ReadOnly: true},
		{Host: "/s/recorder/ONS-9", Guest: "/var/log/homelab-recorder"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(args, " ")
	want := "machine create --error-format json --name m --from /p.smolmachine --nested" +
		" -v /c/sources:/home/builder/.cache/homelab-sources:ro -v /s/recorder/ONS-9:/var/log/homelab-recorder"
	if got != want {
		t.Errorf("args = %s\nwant %s", got, want)
	}
	if _, err := CreateArgs(Spec{Name: "m", Pack: "/p", Mounts: []Mount{{Host: "/a", Guest: "/b:c"}}}); err == nil {
		t.Error("a guest path with ':' should be refused")
	}

	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "smolvm")
	script := "#!/bin/sh\necho \"$SMOLVM_LIB_DIR $1 $2\" >> " + log + "\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	s := &Smolvm{Binary: bin}
	ctx := context.Background()
	m, err := s.Create(ctx, Spec{Name: "m", Pack: "/p", HostEnv: []string{"SMOLVM_LIB_DIR=/k"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Exec(ctx, Exec{Argv: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if !strings.HasPrefix(line, "/k ") {
			t.Errorf("a smolvm call without the machine's host environment: %q", line)
		}
	}
}
