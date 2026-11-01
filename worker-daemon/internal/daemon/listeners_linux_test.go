package daemon_test

import (
	"bufio"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// socketInodes returns the inodes of this process's open sockets.
func socketInodes(t *testing.T) map[string]bool {
	t.Helper()
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, fd := range fds {
		link, err := os.Readlink(filepath.Join("/proc/self/fd", fd.Name()))
		if err == nil && strings.HasPrefix(link, "socket:[") {
			out[strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]")] = true
		}
	}
	return out
}

// procIP decodes an address of /proc/net/tcp or tcp6: the kernel
// prints each 32-bit word in host (little-endian) order.
func procIP(s string) net.IP {
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	for i := 0; i+4 <= len(b); i += 4 {
		b[i], b[i+1], b[i+2], b[i+3] = b[i+3], b[i+2], b[i+1], b[i]
	}
	return net.IP(b)
}

// tcpListeners returns the local IP of every TCP socket this process
// listens on.
func tcpListeners(t *testing.T, mine map[string]bool) []net.IP {
	t.Helper()
	var out []net.IP
	for _, file := range []string{"/proc/self/net/tcp", "/proc/self/net/tcp6"} {
		f, err := os.Open(file)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 10 || fields[3] != "0A" || !mine[fields[9]] {
				continue
			}
			host, _, _ := strings.Cut(fields[1], ":")
			out = append(out, procIP(host))
		}
		_ = f.Close()
	}
	return out
}

// unixListeners returns the path of every Unix socket this process
// listens on.
func unixListeners(t *testing.T, mine map[string]bool) []string {
	t.Helper()
	f, err := os.Open("/proc/self/net/unix")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Scan()
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Num RefCount Protocol Flags Type St Inode [Path]; flag 0x10000
		// is a listening socket.
		if len(fields) < 7 || fields[3] != "00010000" || !mine[fields[6]] {
			continue
		}
		path := ""
		if len(fields) > 7 {
			path = fields[7]
		}
		out = append(out, path)
	}
	return out
}

// Scenario: No listener on the host network. While the daemon runs a
// job, every socket it listens on is bound to loopback or to that
// job's guest network: the forwarders are Unix sockets in the job's
// directory, which only its machine is given. The fake control plane
// and the fake machine's guest ports in this process are on loopback.
func TestNoListenerOnTheHostNetwork(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_MODE": "sleep"})
	h.waitFor("the agent to run", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "sleeping")
	})
	jobDir := h.d.JobDir("ONS-7", 1)

	own := h.d.Listeners()
	if len(own) != 2 {
		t.Fatalf("the daemon lists %d listeners, want the egress and MCP forwarders: %v", len(own), own)
	}
	for _, a := range own {
		if a.Network() != "unix" || filepath.Dir(a.String()) != jobDir {
			t.Errorf("listener %s %s is not a Unix socket in the job's directory %s", a.Network(), a, jobDir)
		}
	}

	mine := socketInodes(t)
	tcp := tcpListeners(t, mine)
	if len(tcp) == 0 {
		t.Fatal("found no TCP listener at all; the fake control plane should be one")
	}
	for _, ip := range tcp {
		if !ip.IsLoopback() {
			t.Errorf("a TCP socket listens on %s, which is not loopback", ip)
		}
	}
	unix := unixListeners(t, mine)
	if len(unix) != 2 {
		t.Errorf("the process listens on Unix sockets %q, want the two forwarders", unix)
	}
	for _, p := range unix {
		if filepath.Dir(p) != jobDir {
			t.Errorf("a Unix socket listens at %q, outside the job's directory %s", p, jobDir)
		}
	}
}
