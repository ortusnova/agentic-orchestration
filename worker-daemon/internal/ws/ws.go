// Package ws is the small part of WebSocket (RFC 6455) the daemon needs
// to carry an agent's standard input and output on the stdio flow: a
// client, and a server for tests and the fake control plane. It has no
// extensions and no subprotocols.
package ws

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6455 fixes SHA-1 for the handshake
	"crypto/tls"
	"encoding/base64"
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

// Opcodes of data frames.
const (
	OpText   = 1
	OpBinary = 2

	opContinuation = 0
	opClose        = 8
	opPing         = 9
	opPong         = 10
)

// MaxMessage bounds one message.
const MaxMessage = 16 << 20

// ErrClosed reports a connection the peer closed.
var ErrClosed = errors.New("ws: closed")

const acceptGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func acceptKey(key string) string {
	h := sha1.Sum([]byte(key + acceptGUID)) // #nosec G401 -- RFC 6455
	return base64.StdEncoding.EncodeToString(h[:])
}

// Conn is one WebSocket connection. Reads are for one goroutine;
// writes may come from several.
type Conn struct {
	conn   net.Conn
	br     *bufio.Reader
	client bool

	wmu       sync.Mutex
	closeOnce sync.Once
}

// Dial opens a WebSocket to rawURL (ws or wss) with header added to the
// handshake. tlsConfig, when not nil, configures a wss connection.
func Dial(ctx context.Context, rawURL string, header http.Header, tlsConfig *tls.Config) (*Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("ws: %w", err)
	}
	host := u.Host
	switch u.Scheme {
	case "ws":
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	case "wss":
		if u.Port() == "" {
			host = net.JoinHostPort(u.Hostname(), "443")
		}
	default:
		return nil, fmt.Errorf("ws: %s: scheme must be ws or wss", u.Redacted())
	}
	var d net.Dialer
	nc, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("ws: %w", err)
	}
	if u.Scheme == "wss" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		tc := tls.Client(nc, cfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = nc.Close()
			return nil, fmt.Errorf("ws: %w", err)
		}
		nc = tc
	}
	stop := context.AfterFunc(ctx, func() { _ = nc.Close() })
	defer stop()
	nonce := make([]byte, 16)
	_, _ = rand.Read(nonce)
	key := base64.StdEncoding.EncodeToString(nonce)
	req := &http.Request{Method: http.MethodGet, URL: u, Host: u.Host, Header: http.Header{}, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1}
	for k, vs := range header {
		req.Header[k] = append([]string(nil), vs...)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	reqURL := *u
	reqURL.Scheme = map[string]string{"ws": "http", "wss": "https"}[u.Scheme]
	req.URL = &reqURL
	if err := req.Write(nc); err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("ws: handshake: %w", err)
	}
	br := bufio.NewReader(nc)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("ws: handshake: %w", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = nc.Close()
		return nil, &HandshakeError{Status: resp.StatusCode}
	}
	if resp.Header.Get("Sec-WebSocket-Accept") != acceptKey(key) {
		_ = nc.Close()
		return nil, errors.New("ws: handshake: wrong Sec-WebSocket-Accept")
	}
	if ctx.Err() != nil {
		_ = nc.Close()
		return nil, ctx.Err()
	}
	return &Conn{conn: nc, br: br, client: true}, nil
}

// HandshakeError reports a server that refused the upgrade.
type HandshakeError struct{ Status int }

func (e *HandshakeError) Error() string {
	return fmt.Sprintf("ws: handshake refused with HTTP %d", e.Status)
}

// Accept upgrades a server request.
func Accept(w http.ResponseWriter, r *http.Request) (*Conn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || r.Header.Get("Sec-WebSocket-Key") == "" {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return nil, errors.New("ws: not an upgrade request")
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "cannot upgrade", http.StatusInternalServerError)
		return nil, errors.New("ws: response cannot be hijacked")
	}
	nc, rw, err := hj.Hijack()
	if err != nil {
		return nil, fmt.Errorf("ws: %w", err)
	}
	_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", acceptKey(r.Header.Get("Sec-WebSocket-Key")))
	if err == nil {
		err = rw.Flush()
	}
	if err != nil {
		_ = nc.Close()
		return nil, fmt.Errorf("ws: %w", err)
	}
	return &Conn{conn: nc, br: rw.Reader}, nil
}

// ReadMessage returns the next data message. It answers pings, and
// returns [ErrClosed] once the peer closes.
func (c *Conn) ReadMessage() (int, []byte, error) {
	var (
		op  int
		msg []byte
	)
	for {
		fin, fop, payload, err := c.readFrame()
		if err != nil {
			return 0, nil, err
		}
		switch fop {
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return 0, nil, err
			}
			continue
		case opPong:
			continue
		case opClose:
			_ = c.writeFrame(opClose, payload)
			_ = c.conn.Close()
			return 0, nil, ErrClosed
		case opContinuation:
			if op == 0 {
				return 0, nil, errors.New("ws: continuation without a message")
			}
		case OpText, OpBinary:
			if op != 0 {
				return 0, nil, errors.New("ws: new message inside a fragmented one")
			}
			op = fop
		default:
			return 0, nil, fmt.Errorf("ws: unknown opcode %d", fop)
		}
		if len(msg)+len(payload) > MaxMessage {
			return 0, nil, errors.New("ws: message too large")
		}
		msg = append(msg, payload...)
		if fin {
			return op, msg, nil
		}
	}
}

func (c *Conn) readFrame() (bool, int, []byte, error) {
	var h [2]byte
	if _, err := io.ReadFull(c.br, h[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
			return false, 0, nil, ErrClosed
		}
		return false, 0, nil, err
	}
	fin := h[0]&0x80 != 0
	op := int(h[0] & 0x0f)
	masked := h[1]&0x80 != 0
	n := uint64(h[1] & 0x7f)
	switch n {
	case 126:
		var b [2]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = uint64(binary.BigEndian.Uint16(b[:]))
	case 127:
		var b [8]byte
		if _, err := io.ReadFull(c.br, b[:]); err != nil {
			return false, 0, nil, err
		}
		n = binary.BigEndian.Uint64(b[:])
	}
	if n > MaxMessage {
		return false, 0, nil, errors.New("ws: frame too large")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(c.br, mask[:]); err != nil {
			return false, 0, nil, err
		}
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(c.br, payload); err != nil {
		return false, 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return fin, op, payload, nil
}

// WriteMessage sends one data message.
func (c *Conn) WriteMessage(op int, data []byte) error {
	if op != OpText && op != OpBinary {
		return errors.New("ws: not a data opcode")
	}
	return c.writeFrame(op, data)
}

func (c *Conn) writeFrame(op int, data []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	buf := make([]byte, 0, 14+len(data))
	buf = append(buf, 0x80|byte(op)) // #nosec G115 -- a data or control opcode
	var maskBit byte
	if c.client {
		maskBit = 0x80
	}
	switch n := len(data); {
	case n < 126:
		buf = append(buf, maskBit|byte(n)) // #nosec G115 -- n < 126
	case n <= 0xffff:
		buf = append(buf, maskBit|126)
		buf = binary.BigEndian.AppendUint16(buf, uint16(n))
	default:
		buf = append(buf, maskBit|127)
		buf = binary.BigEndian.AppendUint64(buf, uint64(n))
	}
	if c.client {
		var mask [4]byte
		_, _ = rand.Read(mask[:])
		buf = append(buf, mask[:]...)
		start := len(buf)
		buf = append(buf, data...)
		for i := range data {
			buf[start+i] ^= mask[i%4]
		}
	} else {
		buf = append(buf, data...)
	}
	_, err := c.conn.Write(buf)
	return err
}

// Close sends a normal close and closes the connection.
func (c *Conn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		_ = c.writeFrame(opClose, []byte{0x03, 0xe8})
		err = c.conn.Close()
	})
	return err
}
