package registry

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "reg")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func listen(t *testing.T, path string) {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
}

// deadPID returns the ID of a process that has exited and been reaped.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestKeys(t *testing.T) {
	t.Parallel()

	tests := []struct{ identifier, workdir, want string }{
		{"PROJ-12", "/w/whatever", "PROJ-12"},
		{"owner/repo#9", "/w/x", "owner_repo_9"},
		{"../../etc", "/w/x", "______etc"},
		{"", "/ws/LIVE-1", "LIVE-1"},
		{"  ", "/", "session"},
	}
	for _, tt := range tests {
		if got := KeyFor(tt.identifier, tt.workdir); got != tt.want {
			t.Errorf("KeyFor(%q, %q) = %q, want %q", tt.identifier, tt.workdir, got, tt.want)
		}
	}
}

func TestRecord_RoundTripsThroughList(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix sockets")
	}

	dir := shortDir(t)
	pid := os.Getpid()
	socket := SocketPath(dir, "LIVE-1", pid)
	listen(t, socket)
	rec := Record{
		Key: "LIVE-1", PID: pid, Socket: socket, HTTPURL: "http://127.0.0.1:7700",
		Issue: "LIVE-1", Rule: "implement", ChainID: "c-42", Attempt: "1",
		Agent: Agent{Name: "opencode", Version: "1.18.29"}, SessionID: "ses_1", StartedAt: time.Now().UTC().Truncate(time.Second),
	}
	if err := Write(dir, rec); err != nil {
		t.Fatal(err)
	}
	got, err := List(dir, "LIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != rec {
		t.Fatalf("List = %+v, want exactly %+v", got, rec)
	}
	if info, err := os.Stat(filepath.Join(dir, "LIVE-1."+strconv.Itoa(pid)+".json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("record file mode = %v, %v; want 0600", info, err)
	}
}

func TestRemove_TouchesOnlyItsOwnBroker(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix sockets")
	}

	dir := shortDir(t)
	self := os.Getpid()
	other := os.Getppid() // a second live process for the same key
	for _, pid := range []int{self, other} {
		listen(t, SocketPath(dir, "LIVE-1", pid))
		if err := Write(dir, Record{Key: "LIVE-1", PID: pid, Socket: SocketPath(dir, "LIVE-1", pid), StartedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}

	Remove(dir, "LIVE-1", self)

	got, err := List(dir, "LIVE-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PID != other {
		t.Fatalf("List after removing one broker = %+v, want only pid %d", got, other)
	}
	if _, err := os.Stat(SocketPath(dir, "LIVE-1", self)); !os.IsNotExist(err) {
		t.Error("the removed broker's socket is still there")
	}
	if _, err := os.Stat(SocketPath(dir, "LIVE-1", other)); err != nil {
		t.Errorf("the other broker's socket is gone: %v", err)
	}
}

func TestList_RemovesStaleRecords(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses signals to probe processes")
	}

	dir := shortDir(t)
	pid := deadPID(t)
	socket := SocketPath(dir, "LIVE-1", pid)
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(dir, Record{Key: "LIVE-1", PID: pid, Socket: socket, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	got, err := List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("List = %+v, want no stale record", got)
	}
	for _, p := range []string{socket, filepath.Join(dir, "LIVE-1."+strconv.Itoa(pid)+".json")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived the listing of a stale record", p)
		}
	}
}

func TestList_NewestFirstAndSkipsDeafSockets(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("uses Unix sockets")
	}

	dir := shortDir(t)
	self, parent := os.Getpid(), os.Getppid()
	listen(t, SocketPath(dir, "a", self))
	listen(t, SocketPath(dir, "b", parent))
	now := time.Now().UTC()
	_ = Write(dir, Record{Key: "a", PID: self, Socket: SocketPath(dir, "a", self), StartedAt: now})
	_ = Write(dir, Record{Key: "b", PID: parent, Socket: SocketPath(dir, "b", parent), StartedAt: now.Add(time.Minute)})
	// A live process whose socket accepts nothing is not listed, and kept.
	_ = Write(dir, Record{Key: "c", PID: self, Socket: SocketPath(dir, "c", self), StartedAt: now})

	got, err := List(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Key != "b" || got[1].Key != "a" {
		t.Errorf("List = %+v, want b then a", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "c."+strconv.Itoa(self)+".json")); err != nil {
		t.Errorf("record of a live process was removed: %v", err)
	}
}

// A registry directory deep enough that KEY.PID.sock would not fit in a
// socket address (it happened on p1 once PIDs reached seven digits) gets a
// short, stable path in the runtime directory instead.
func TestSocketPathFitsASocketAddress(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)

	short := SocketPath("/run/reg", "planning-d8481392", 1745717)
	if short != "/run/reg/planning-d8481392.1745717.sock" {
		t.Fatalf("a short path changed: %s", short)
	}

	deep := "/home/brian/.local/share/ortus-nova/stage0/sessions/ortus-nova-specs/registry"
	got := SocketPath(deep, "planning-d8481392", 1745717)
	if len(got) > maxSocketPath() {
		t.Fatalf("socket path is %d bytes, over %d: %s", len(got), maxSocketPath(), got)
	}
	if filepath.Dir(got) != runtimeDir {
		t.Fatalf("long path not moved to the runtime directory: %s", got)
	}
	if again := SocketPath(deep, "planning-d8481392", 1745717); again != got {
		t.Fatalf("not stable: %s then %s", got, again)
	}
	if other := SocketPath(deep, "planning-d8481392", 1745718); other == got {
		t.Fatalf("two brokers share a socket path: %s", got)
	}

	// The broker listens there for real.
	ln, err := net.Listen("unix", got)
	if err != nil {
		t.Fatalf("listen on %s: %v", got, err)
	}
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}
