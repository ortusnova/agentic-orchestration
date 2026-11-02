// Package machine runs a job's machine: create it with its shared
// directory and forwarders, run commands in it, and stop and delete it.
package machine

import (
	"context"
	"io"
	"os/exec"
)

// Guest paths every backend provides.
const (
	// GuestWorkspace is the agent's working tree.
	GuestWorkspace = "/workspace"
	// GuestShare is the job's shared directory: the workspace seed in,
	// the exported bundle out. No token is ever written there.
	GuestShare = "/run/worker"
)

// Forward joins a guest loopback port to a host Unix socket the daemon
// serves. It is the machine's only way off the guest: a machine has no
// network of its own.
type Forward struct {
	Name       string
	HostSocket string
	GuestPort  int
}

// Spec describes a machine.
type Spec struct {
	Name string
	// Pack is the pack file the machine is created from.
	Pack string
	// User is the user commands run as; "" keeps the pack's.
	User    string
	CPUs    int
	MemMiB  int
	DiskGiB int
	Labels  map[string]string
	// Share is the host directory mounted at [GuestShare].
	Share    string
	Forwards []Forward
}

// Exec is one command in the machine.
type Exec struct {
	Argv []string
	// Env holds KEY=VALUE entries for the command.
	Env     []string
	Workdir string
	// Stdin, when not nil, is copied to the command's standard input,
	// which closes at its end.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Backend creates machines.
type Backend interface {
	// Name is the run.backend value the backend serves.
	Name() string
	// Available reports whether the backend can run machines here.
	Available(ctx context.Context) error
	// Create creates and starts a machine with its forwarders up.
	Create(ctx context.Context, spec Spec) (Machine, error)
}

// Machine is one running machine.
type Machine interface {
	Name() string
	// Exec runs a command and returns its exit status. Cancelling ctx
	// ends the command.
	Exec(ctx context.Context, e Exec) (int, error)
	// GuestAddr is the host:port, inside the guest, of a forward.
	GuestAddr(forward string) string
	// Stop stops the machine; its disk stays.
	Stop(ctx context.Context) error
	// Delete deletes the machine and its disk.
	Delete(ctx context.Context) error
}

// StartStdin gives cmd a standard input fed from r by a goroutine that
// the command's end does not wait for: a stream such as the stdio flow
// stays open after the agent exits, and the caller closes r then.
func StartStdin(cmd *exec.Cmd, r io.Reader) error {
	if r == nil {
		return nil
	}
	w, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	go func() {
		_, _ = io.Copy(w, r)
		_ = w.Close()
	}()
	return nil
}
