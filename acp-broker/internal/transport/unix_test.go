package transport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/transport"
)

// lineEnd is one side of a newline-delimited JSON pipe.
type lineEnd struct {
	t     *testing.T
	w     io.Writer
	lines chan map[string]any
}

func newLineEnd(t *testing.T, r io.Reader, w io.Writer) *lineEnd {
	e := &lineEnd{t: t, w: w, lines: make(chan map[string]any, 256)}
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if json.Unmarshal(sc.Bytes(), &m) == nil {
				e.lines <- m
			}
		}
		close(e.lines)
	}()
	return e
}

func (e *lineEnd) send(v any) {
	e.t.Helper()
	data, _ := json.Marshal(v)
	if _, err := e.w.Write(append(data, '\n')); err != nil {
		e.t.Fatal(err)
	}
}

func (e *lineEnd) nextWhere(match func(map[string]any) bool) map[string]any {
	e.t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case m, ok := <-e.lines:
			if !ok {
				e.t.Fatal("closed")
			}
			if match(m) {
				return m
			}
		case <-timeout:
			e.t.Fatal("timed out")
		}
	}
}

type session struct {
	b      *broker.Broker
	driver *lineEnd
	agent  *lineEnd
}

// runSession starts a broker on pipes. With create set, it creates
// session "s1" and starts driver prompt 2.
func runSession(t *testing.T, cfg broker.Config, create bool) *session {
	t.Helper()
	driverInR, driverInW := io.Pipe()
	driverOutR, driverOutW := io.Pipe()
	agentInR, agentInW := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	cfg.DriverIn, cfg.DriverOut, cfg.AgentIn, cfg.AgentOut = driverInR, driverOutW, agentInW, agentOutR
	b := broker.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx) }()
	s := &session{b: b, driver: newLineEnd(t, driverOutR, driverInW), agent: newLineEnd(t, agentInR, agentOutW)}
	if create {
		s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{}})
		s.agent.nextWhere(func(map[string]any) bool { return true })
		s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"sessionId": "s1"}})
		s.driver.nextWhere(func(map[string]any) bool { return true })
		s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
		s.agent.nextWhere(func(m map[string]any) bool { return m["method"] == "session/prompt" })
	}
	return s
}

func shortSocketDir(t *testing.T) string {
	t.Helper()
	// Unix socket paths are short; t.TempDir can exceed the limit.
	dir, err := os.MkdirTemp("", "acpb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestListenUnix_OwnerOnlyPermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}

	base := shortSocketDir(t)
	dir := filepath.Join(base, "acp-broker")
	if err := os.Mkdir(dir, 0o755); err != nil { // a looser directory is narrowed
		t.Fatal(err)
	}
	path := filepath.Join(dir, "k.1.sock")
	ln, err := transport.ListenUnix(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	for p, want := range map[string]os.FileMode{dir: 0o700, path: 0o600} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", p, got, want)
		}
	}
}

func dialViewer(t *testing.T, b *broker.Broker) *lineEnd {
	t.Helper()
	path := filepath.Join(shortSocketDir(t), "v.sock")
	ln, err := transport.ListenUnix(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() { _ = transport.ServeUnix(ln, b) }()
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	v := newLineEnd(t, conn, conn)
	v.nextWhere(func(m map[string]any) bool { return m["type"] == "state" })
	return v
}

func TestSocket_RejectedCommandsNameTheReasonAndChangeNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		line    string
		session bool
		want    string
	}{
		{"before a session exists", `{"op":"takeover"}`, false, "no session yet"},
		{"not JSON", `takeover please`, true, "bad command"},
		{"unknown op", `{"op":"explode"}`, true, "unknown op"},
		{"steer without text", `{"op":"steer","text":" "}`, true, "steer needs text"},
		{"prompt without the floor", `{"op":"prompt","text":"hi"}`, true, "interrupt before prompting"},
		{"handback without an interrupt", `{"op":"handback"}`, true, "not interrupted"},
		{"permit an unknown request", `{"op":"permit","id":"nope","option":"allow_once"}`, true, "no waiting permission request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := runSession(t, broker.Config{}, tt.session)
			v := dialViewer(t, s.b)
			before := s.b.State()

			if _, err := io.WriteString(v.w, tt.line+"\n"); err != nil {
				t.Fatal(err)
			}
			got := v.nextWhere(func(m map[string]any) bool { return m["type"] == "error" })
			if msg, _ := got["message"].(string); !strings.Contains(msg, tt.want) {
				t.Errorf("error = %v, want a message naming %q", got, tt.want)
			}
			if seq, _ := got["seq"].(float64); seq != 0 {
				t.Errorf("error seq = %v, want 0: a reply is not part of the event log", got["seq"])
			}
			after := s.b.State()
			if before.Floor != after.Floor || before.DriverTurn != after.DriverTurn {
				t.Errorf("state changed from %+v to %+v", before, after)
			}
		})
	}
}

func TestSocket_InterruptTwiceAndPromptWhileATurnRuns(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	v := dialViewer(t, s.b)
	v.send(broker.Command{Op: "interrupt"})
	s.agent.nextWhere(func(m map[string]any) bool { return m["method"] == "session/cancel" })

	v.send(broker.Command{Op: "prompt", Text: "too early"})
	if got := v.nextWhere(func(m map[string]any) bool { return m["type"] == "error" }); !strings.Contains(got["message"].(string), "has not ended yet") {
		t.Errorf("prompt before the cancelled turn ended = %v", got)
	}
	// Interrupting again is not an error: the floor simply stays with humans.
	v.send(broker.Command{Op: "interrupt"})
	if got := v.nextWhere(func(m map[string]any) bool { return m["type"] == "state" || m["type"] == "error" }); got["type"] != "state" || got["floor"] != "human" {
		t.Errorf("second interrupt = %v, want the state with humans on the floor", got)
	}
}
