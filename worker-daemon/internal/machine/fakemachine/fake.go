// Package fakemachine is a machine backend for tests. A machine is a
// directory on this host: its commands run as host processes with only
// the environment they are given, and the guest paths
// [machine.GuestWorkspace] and [machine.GuestShare] map into it. Each
// forward listens on host loopback. It records everything a guest is
// given, so a test can look for what must not be there.
package fakemachine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/machine"
)

// Backend creates fake machines under Dir.
type Backend struct {
	Dir string
	// BackendName is the backend the fake stands in for; default smolvm.
	BackendName string

	mu       sync.Mutex
	machines []*Machine
}

// Name implements [machine.Backend].
func (b *Backend) Name() string {
	if b.BackendName == "" {
		return "smolvm"
	}
	return b.BackendName
}

// Available implements [machine.Backend].
func (b *Backend) Available(context.Context) error { return nil }

// Machines returns every machine created so far.
func (b *Backend) Machines() []*Machine {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]*Machine(nil), b.machines...)
}

// Machine returns the machine named name, or nil.
func (b *Backend) Machine(name string) *Machine {
	for _, m := range b.Machines() {
		if m.spec.Name == name {
			return m
		}
	}
	return nil
}

// Create implements [machine.Backend].
func (b *Backend) Create(_ context.Context, spec machine.Spec) (machine.Machine, error) {
	root := filepath.Join(b.Dir, spec.Name)
	for _, d := range []string{"workspace", "home"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o750); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Machine{spec: spec, root: root, ctx: ctx, cancel: cancel, addrs: map[string]string{}}
	for _, f := range spec.Forwards {
		var lc net.ListenConfig
		ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			m.shutdown()
			return nil, err
		}
		m.listeners = append(m.listeners, ln)
		m.addrs[f.Name] = ln.Addr().String()
		go serveForward(ln, f.HostSocket)
	}
	b.mu.Lock()
	b.machines = append(b.machines, m)
	b.mu.Unlock()
	return m, nil
}

func serveForward(ln net.Listener, socket string) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer func() { _ = c.Close() }()
			var d net.Dialer
			u, err := d.DialContext(context.Background(), "unix", socket)
			if err != nil {
				return
			}
			defer func() { _ = u.Close() }()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(u, c); done <- struct{}{} }()
			go func() { _, _ = io.Copy(c, u); done <- struct{}{} }()
			<-done
		}()
	}
}

// ExecRecord is one command a machine was asked to run, as the guest
// sees it.
type ExecRecord struct {
	Argv    []string
	Env     []string
	Workdir string
}

// Machine is a fake machine.
type Machine struct {
	spec      machine.Spec
	root      string
	ctx       context.Context
	cancel    context.CancelFunc
	listeners []net.Listener
	addrs     map[string]string

	mu        sync.Mutex
	execs     []ExecRecord
	stoppedAt time.Time
	deleted   bool
}

// Spec returns the spec the machine was created with.
func (m *Machine) Spec() machine.Spec { return m.spec }

// Root is the machine's directory.
func (m *Machine) Root() string { return m.root }

// Execs returns every command run so far.
func (m *Machine) Execs() []ExecRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ExecRecord(nil), m.execs...)
}

// StoppedAt is when the machine was stopped or deleted; zero while it
// runs.
func (m *Machine) StoppedAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stoppedAt
}

// Deleted reports whether the machine was deleted.
func (m *Machine) Deleted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.deleted
}

// Name implements [machine.Machine].
func (m *Machine) Name() string { return m.spec.Name }

// GuestAddr implements [machine.Machine].
func (m *Machine) GuestAddr(forward string) string { return m.addrs[forward] }

// HostPath maps a guest path into the machine's directory.
func (m *Machine) HostPath(guest string) string {
	return m.mapPaths(guest)
}

func (m *Machine) mapPaths(s string) string {
	s = strings.ReplaceAll(s, machine.GuestWorkspace, filepath.Join(m.root, "workspace"))
	if m.spec.Share != "" {
		s = strings.ReplaceAll(s, machine.GuestShare, m.spec.Share)
	}
	return s
}

// Exec implements [machine.Machine].
func (m *Machine) Exec(ctx context.Context, e machine.Exec) (int, error) {
	m.mu.Lock()
	m.execs = append(m.execs, ExecRecord{Argv: append([]string(nil), e.Argv...), Env: append([]string(nil), e.Env...), Workdir: e.Workdir})
	stopped := !m.stoppedAt.IsZero()
	m.mu.Unlock()
	if stopped {
		return -1, errors.New("fakemachine: machine is stopped")
	}
	if len(e.Argv) == 0 {
		return -1, errors.New("fakemachine: empty command")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	argv := make([]string, len(e.Argv))
	for i, a := range e.Argv {
		argv[i] = m.mapPaths(a)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- a test backend runs what it is given
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(m.root, "home")}
	for _, kv := range e.Env {
		cmd.Env = append(cmd.Env, m.mapPaths(kv))
	}
	cmd.Dir = filepath.Join(m.root, "home")
	if e.Workdir != "" {
		cmd.Dir = m.mapPaths(e.Workdir)
	}
	cmd.Stdout, cmd.Stderr = e.Stdout, e.Stderr
	cmd.WaitDelay = 2 * time.Second
	if err := machine.StartStdin(cmd, e.Stdin); err != nil {
		return -1, err
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, fmt.Errorf("fakemachine: %w", err)
	}
	return 0, nil
}

func (m *Machine) shutdown() {
	m.cancel()
	for _, ln := range m.listeners {
		_ = ln.Close()
	}
}

// Stop implements [machine.Machine]: every command ends.
func (m *Machine) Stop(context.Context) error {
	m.shutdown()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stoppedAt.IsZero() {
		m.stoppedAt = time.Now()
	}
	return nil
}

// Delete implements [machine.Machine]. The directory stays, so a test
// can read what the guest held.
func (m *Machine) Delete(ctx context.Context) error {
	_ = m.Stop(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = true
	return nil
}
