package machine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Smolvm drives the smolvm CLI. A machine gets no credential binding.
// Without a [Network] it gets no network flag and no allowlist, so
// smolvm gives it no network: its only way out is each forward, a host
// socket mounted with --mount-socket and served on guest loopback by
// socat. With one, it also gets smolvm's virtio-net stack, which
// reaches only the network's hosts, UDP and private entries.
type Smolvm struct {
	// Binary is the smolvm program.
	Binary string
	// ReadyTries and ReadyWait bound the wait for the guest to run
	// commands after a start.
	ReadyTries int
	ReadyWait  time.Duration
}

// Name implements [Backend].
func (s *Smolvm) Name() string { return "smolvm" }

// Available implements [Backend]: the binary runs.
func (s *Smolvm) Available(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.Binary, "--version").CombinedOutput() // #nosec G204 -- the operator names the binary
	if err != nil {
		return fmt.Errorf("smolvm: %s --version: %w: %s", s.Binary, err, bytes.TrimSpace(out))
	}
	return nil
}

func machineArgs(sub string, rest ...string) []string {
	return append([]string{"machine", sub, "--error-format", "json"}, rest...)
}

// guestSocket is where a forward's host socket is mounted.
func guestSocket(name string) string { return "/run/worker-" + name + ".sock" }

// CreateArgs returns the smolvm arguments that create spec's machine.
func CreateArgs(spec Spec) ([]string, error) {
	if spec.Name == "" || spec.Pack == "" {
		return nil, errors.New("smolvm: a machine needs a name and a pack")
	}
	args := machineArgs("create", "--name", spec.Name, "--from", spec.Pack)
	for _, sz := range []struct {
		flag string
		v    int
	}{{"--cpus", spec.CPUs}, {"--mem", spec.MemMiB}, {"--storage", spec.DiskGiB}} {
		if sz.v > 0 {
			args = append(args, sz.flag, strconv.Itoa(sz.v))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(spec.Labels)) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User, "--init", "chown "+spec.User+" "+GuestWorkspace)
	}
	if spec.Nested {
		args = append(args, "--nested")
	}
	netArgs, err := networkArgs(spec.Network)
	if err != nil {
		return nil, err
	}
	args = append(args, netArgs...)
	var paths []string
	if spec.Share != "" {
		paths = append(paths, spec.Share)
	}
	for _, f := range spec.Forwards {
		paths = append(paths, f.HostSocket)
	}
	for _, m := range spec.Mounts {
		paths = append(paths, m.Host, m.Guest)
	}
	for _, p := range paths {
		if p == "" || strings.Contains(p, ":") {
			return nil, fmt.Errorf("smolvm: mount path %q is empty or contains ':'", p)
		}
	}
	if spec.Share != "" {
		args = append(args, "-v", spec.Share+":"+GuestShare)
	}
	for _, m := range spec.Mounts {
		v := m.Host + ":" + m.Guest
		if m.ReadOnly {
			v += ":ro"
		}
		args = append(args, "-v", v)
	}
	for _, f := range spec.Forwards {
		args = append(args, "--mount-socket", f.HostSocket+":"+guestSocket(f.Name))
	}
	return args, nil
}

// networkArgs are the flags of design D8 for n: virtio-net with private
// ranges blocked, and each host by name, each UDP entry and each private
// entry. Never --allow-host, which takes a host's addresses at the
// machine's start, and never --net, which opens the network unfiltered.
func networkArgs(n *Network) ([]string, error) {
	if n == nil {
		return nil, nil
	}
	if len(n.Hosts) == 0 {
		return nil, errors.New("smolvm: a network with no host pattern: --block-private alone leaves the internet open")
	}
	args := []string{"--net-backend", "virtio-net", "--block-private"}
	for _, l := range []struct {
		flag string
		vs   []string
	}{{"--allow-host-pattern", n.Hosts}, {"--allow-udp", n.UDP}, {"--allow-private", n.Private}} {
		for _, v := range l.vs {
			if v == "" || strings.HasPrefix(v, "-") || strings.ContainsAny(v, " \t\n,") {
				return nil, fmt.Errorf("smolvm: %s value %q is empty or not one entry", l.flag, v)
			}
			args = append(args, l.flag, v)
		}
	}
	return args, nil
}

// ExecArgs returns the smolvm arguments that run e in machine name.
func ExecArgs(name string, e Exec) []string {
	args := machineArgs("exec", "--name", name)
	if e.Stdin != nil {
		args = append(args, "-i")
	} else {
		args = append(args, "--stream")
	}
	if e.Workdir != "" {
		args = append(args, "-w", e.Workdir)
	}
	for _, kv := range e.Env {
		args = append(args, "-e", kv)
	}
	return append(append(args, "--"), e.Argv...)
}

// ForwardArgv is the guest command that serves a forward on loopback.
func ForwardArgv(f Forward) []string {
	return []string{"socat", "TCP-LISTEN:" + strconv.Itoa(f.GuestPort) + ",bind=127.0.0.1,reuseaddr,fork", "UNIX-CONNECT:" + guestSocket(f.Name)}
}

func (s *Smolvm) run(ctx context.Context, env, args []string) error {
	cmd := exec.CommandContext(ctx, s.Binary, args...) // #nosec G204 -- argv, no shell
	cmd.Env = withEnv(env)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("smolvm %s %s: %w: %s", args[0], args[1], err, tail(stderr.String()))
	}
	return nil
}

// withEnv is the daemon's environment with env added, or nil, the
// daemon's own, when env is empty.
func withEnv(env []string) []string {
	if len(env) == 0 {
		return nil
	}
	return append(os.Environ(), env...)
}

// List implements [Lister] with smolvm machine ls --json. It reads the
// name, the labels, the state and the creation time of each machine,
// under the names smolvm has used for them, and leaves the time zero
// when smolvm gives none.
func (s *Smolvm) List(ctx context.Context) ([]Listed, error) {
	cmd := exec.CommandContext(ctx, s.Binary, "machine", "ls", "--json") // #nosec G204 -- argv, no shell
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("smolvm machine ls: %w: %s", err, tail(stderr.String()))
	}
	return ParseList(out)
}

// ParseList reads the output of smolvm machine ls --json.
func ParseList(out []byte) ([]Listed, error) {
	var raw []map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("smolvm machine ls --json: %w", err)
	}
	str := func(m map[string]json.RawMessage, keys ...string) string {
		for _, k := range keys {
			var v string
			if json.Unmarshal(m[k], &v) == nil && v != "" {
				return v
			}
		}
		return ""
	}
	listed := make([]Listed, 0, len(raw))
	for _, m := range raw {
		l := Listed{Name: str(m, "name"), State: str(m, "state", "status")}
		if l.Name == "" {
			continue
		}
		_ = json.Unmarshal(m["labels"], &l.Labels)
		if l.Labels == nil {
			l.Labels = map[string]string{}
		}
		if t, err := time.Parse(time.RFC3339Nano, str(m, "created_at", "created", "createdAt")); err == nil {
			l.Created = t
		} else {
			var unix int64
			if json.Unmarshal(m["created_at"], &unix) == nil && unix > 0 {
				l.Created = time.Unix(unix, 0)
			}
		}
		listed = append(listed, l)
	}
	return listed, nil
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2048 {
		s = s[len(s)-2048:]
	}
	return s
}

// Create implements [Backend]. A machine that fails to come up is
// deleted.
func (s *Smolvm) Create(ctx context.Context, spec Spec) (Machine, error) {
	args, err := CreateArgs(spec)
	if err != nil {
		return nil, err
	}
	if err := s.run(ctx, spec.HostEnv, args); err != nil {
		return nil, err
	}
	m := &smolvmMachine{s: s, name: spec.Name, env: spec.HostEnv, addrs: map[string]string{}}
	fail := func(err error) (Machine, error) {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		_ = m.Delete(dctx)
		return nil, err
	}
	if err := s.run(ctx, m.env, machineArgs("start", "--name", spec.Name)); err != nil {
		return fail(err)
	}
	if err := s.waitReady(ctx, m); err != nil {
		return fail(err)
	}
	for _, f := range spec.Forwards {
		if err := s.run(ctx, m.env, append(append(machineArgs("exec", "--name", spec.Name, "-d"), "--"), ForwardArgv(f)...)); err != nil {
			return fail(err)
		}
		m.addrs[f.Name] = net.JoinHostPort("127.0.0.1", strconv.Itoa(f.GuestPort))
	}
	return m, nil
}

// Reopen implements [Reopener]: a handle on spec's machine, when smolvm
// lists it. Its forwards still run in the guest, so only their addresses
// are taken again.
func (s *Smolvm) Reopen(ctx context.Context, spec Spec) (Machine, error) {
	listed, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range listed {
		if l.Name != spec.Name {
			continue
		}
		m := &smolvmMachine{s: s, name: spec.Name, env: spec.HostEnv, addrs: map[string]string{}}
		for _, f := range spec.Forwards {
			m.addrs[f.Name] = net.JoinHostPort("127.0.0.1", strconv.Itoa(f.GuestPort))
		}
		return m, nil
	}
	return nil, nil
}

// waitReady waits until the guest runs socat: right after a start,
// smolvm can run a command before the pack's root file system is in
// place.
func (s *Smolvm) waitReady(ctx context.Context, m *smolvmMachine) error {
	tries, wait := s.ReadyTries, s.ReadyWait
	if tries <= 0 {
		tries = 20
	}
	if wait <= 0 {
		wait = 250 * time.Millisecond
	}
	var last error
	for range tries {
		code, err := m.Exec(ctx, Exec{Argv: []string{"socat", "-V"}})
		if err == nil && code == 0 {
			return nil
		}
		last = fmt.Errorf("exit %d: %v", code, err)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
	return fmt.Errorf("smolvm: machine %s does not run socat, which its pack needs: %v", m.name, last)
}

type smolvmMachine struct {
	s     *Smolvm
	name  string
	env   []string
	addrs map[string]string
}

func (m *smolvmMachine) Name() string { return m.name }

func (m *smolvmMachine) GuestAddr(forward string) string { return m.addrs[forward] }

func (m *smolvmMachine) Exec(ctx context.Context, e Exec) (int, error) {
	cmd := exec.CommandContext(ctx, m.s.Binary, ExecArgs(m.name, e)...) // #nosec G204 -- argv, no shell
	cmd.Env = withEnv(m.env)
	cmd.Stdout, cmd.Stderr = e.Stdout, e.Stderr
	cmd.WaitDelay = 5 * time.Second
	if err := StartStdin(cmd, e.Stdin); err != nil {
		return -1, err
	}
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return -1, err
	}
	return 0, nil
}

// EgressEvents implements [EgressEventer] with smolvm machine
// egress-events --json.
func (m *smolvmMachine) EgressEvents(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, m.s.Binary, "machine", "egress-events", "--name", m.name, "--json") // #nosec G204 -- argv, no shell
	cmd.Env = withEnv(m.env)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("smolvm machine egress-events: %w: %s", err, tail(stderr.String()))
	}
	return out, nil
}

func (m *smolvmMachine) Stop(ctx context.Context) error {
	return m.s.run(ctx, m.env, machineArgs("stop", "--name", m.name))
}

func (m *smolvmMachine) Delete(ctx context.Context) error {
	return m.s.run(ctx, m.env, machineArgs("delete", "-f", "--name", m.name))
}
