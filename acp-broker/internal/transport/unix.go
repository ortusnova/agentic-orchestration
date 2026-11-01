// Package transport connects human viewers to an ACP broker: newline-
// delimited JSON over a local Unix socket, and Server-Sent Events plus
// POST over HTTP. Every transport turns its connections into the broker's
// viewer interface, so both carry the same events and commands.
package transport

import (
	"bufio"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// ServeUnix accepts viewer connections on ln until it is closed. Each
// connection reads one JSON command per line and receives one JSON event
// per line.
func ServeUnix(ln net.Listener, b *broker.Broker) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go serveConn(conn, b)
	}
}

func serveConn(conn net.Conn, b *broker.Broker) {
	v := b.Attach()

	go func() {
		w := bufio.NewWriter(conn)
		for rec := range v.Lines() {
			if _, err := w.Write(rec.Data); err != nil {
				break
			}
			if len(v.Lines()) == 0 {
				if err := w.Flush(); err != nil {
					break
				}
			}
		}
		_ = w.Flush()
		_ = conn.Close()
	}()

	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			v.CommandLine(line)
		}
		if err != nil {
			v.Close()
			return
		}
	}
}

// ListenUnix listens on a Unix socket at path, readable only by its
// owner: the directory holding it is created, or narrowed, to mode 0700,
// and the socket gets mode 0600. A stale file at path is replaced.
func ListenUnix(ctx context.Context, path string) (net.Listener, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil { //nolint:gosec // a directory needs its search bit
		return nil, err
	}
	_ = os.Remove(path)
	ln, err := (&net.ListenConfig{}).Listen(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}
