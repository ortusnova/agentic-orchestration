// Package stdio is the framed stream of the stdio flow, as Sortie serves
// it: an HTTP/1.1 request upgraded to sortie-stdio/1, then frames both
// ways. A frame is one type byte, a four-byte big-endian payload length
// of at most 1 MiB, and the payload. The control plane sends I (the
// agent's standard input) and C (input closed); the worker sends O
// (standard output), E (standard error) and last X (the agent exited,
// its exit status as a four-byte big-endian signed integer). A stream
// that ends before X is broken.
package stdio

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Protocol is the Upgrade token of the stdio flow.
const Protocol = "sortie-stdio/1"

// Frame types.
const (
	Stdin       byte = 'I'
	StdinClosed byte = 'C'
	Stdout      byte = 'O'
	Stderr      byte = 'E'
	Exit        byte = 'X'
)

// MaxPayload bounds the payload of one frame.
const MaxPayload = 1 << 20

// ErrTooLarge reports a frame whose payload exceeds MaxPayload.
var ErrTooLarge = errors.New("stdio: frame exceeds 1 MiB")

// WriteFrame writes one frame. A payload over MaxPayload is refused.
func WriteFrame(w io.Writer, typ byte, payload []byte) error {
	if len(payload) > MaxPayload {
		return ErrTooLarge
	}
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[1:5], uint32(len(payload))) // #nosec G115 -- bounded by MaxPayload
	copy(b[5:], payload)
	_, err := w.Write(b)
	return err
}

// ExitPayload encodes an exit status.
func ExitPayload(status int) []byte {
	var p [4]byte
	binary.BigEndian.PutUint32(p[:], uint32(int32(status))) // #nosec G115 -- an exit status fits in 32 bits
	return p[:]
}

// ExitStatus decodes the payload of an X frame.
func ExitStatus(payload []byte) (int, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("stdio: exit frame payload of %d bytes, want 4", len(payload))
	}
	return int(int32(binary.BigEndian.Uint32(payload))), nil // #nosec G115 -- decoding ExitPayload
}

// ReadFrame reads one frame.
func ReadFrame(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(head[1:])
	if n > MaxPayload {
		return 0, nil, ErrTooLarge
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return head[0], payload, nil
}

// Conn is one open stdio stream. Reads are for one goroutine; writes
// may come from several.
type Conn struct {
	conn net.Conn
	r    io.Reader

	mu sync.Mutex
}

// NewConn wraps a stream whose upgrade is done; r reads what the peer
// sent, including anything buffered during the upgrade.
func NewConn(conn net.Conn, r io.Reader) *Conn { return &Conn{conn: conn, r: r} }

// ReadFrame reads the next frame from the peer.
func (c *Conn) ReadFrame() (byte, []byte, error) { return ReadFrame(c.r) }

// WriteFrame sends one frame.
func (c *Conn) WriteFrame(typ byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return WriteFrame(c.conn, typ, payload)
}

// Close closes the stream.
func (c *Conn) Close() error { return c.conn.Close() }

// Writer sends each write as frames of one type, split at MaxPayload.
type Writer struct {
	C    *Conn
	Type byte
}

func (w Writer) Write(p []byte) (int, error) {
	n := 0
	for len(p) > 0 {
		k := min(len(p), MaxPayload)
		if err := w.C.WriteFrame(w.Type, p[:k]); err != nil {
			return n, err
		}
		n += k
		p = p[k:]
	}
	return n, nil
}

// RefusedError reports a server that answered the upgrade with another
// status.
type RefusedError struct {
	Status int
	Body   string
}

func (e *RefusedError) Error() string {
	return fmt.Sprintf("stdio: the upgrade was answered HTTP %d: %s", e.Status, e.Body)
}

// Dial opens the stream at rawURL (http or https) with header added to
// the request. tlsConfig, when not nil, configures an https connection.
func Dial(ctx context.Context, rawURL string, header http.Header, tlsConfig *tls.Config) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("stdio: %w", err)
	}
	host := u.Host
	switch u.Scheme {
	case "http":
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	case "https":
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "443")
		}
	default:
		return nil, fmt.Errorf("stdio: %s: scheme must be http or https", u.Redacted())
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("stdio: %w", err)
	}
	if u.Scheme == "https" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		cfg.NextProtos = []string{"http/1.1"}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("stdio: %w", err)
		}
		nc = tc
	}
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host, Header: http.Header{}, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}
	for k, vs := range header {
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", Protocol)
	if err := req.Write(nc); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("stdio: upgrade: %w", err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("stdio: upgrade: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		_ = nc.Close()
		return nil, &RefusedError{Status: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}
	if !strings.EqualFold(resp.Header.Get("Upgrade"), Protocol) {
		_ = nc.Close()
		return nil, fmt.Errorf("stdio: upgrade: the server switched to %q, not %s", resp.Header.Get("Upgrade"), Protocol)
	}
	if ctx.Err() != nil {
		_ = nc.Close()
		return nil, ctx.Err()
	}
	return NewConn(nc, br), nil
}

// Accept takes over r's connection as a stdio stream, as the control
// plane does: it answers 426 to a request without Upgrade: Protocol.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), Protocol) {
		w.Header().Set("Upgrade", Protocol)
		http.Error(w, "the stdio flow needs Upgrade: "+Protocol, http.StatusUpgradeRequired)
		return nil, errors.New("stdio: no upgrade")
	}
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		return nil, err
	}
	if _, err := rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: " + Protocol + "\r\n\r\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return NewConn(conn, rw.Reader), nil
}
