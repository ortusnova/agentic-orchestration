package broker_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/transport"
)

// endpoint reads and writes newline-delimited JSON on one side of a pipe.
type endpoint struct {
	t     *testing.T
	w     io.Writer
	lines chan []byte
}

func newEndpoint(t *testing.T, r io.Reader, w io.Writer) *endpoint {
	e := &endpoint{t: t, w: w, lines: make(chan []byte, 64)}
	go func() {
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadBytes('\n')
			if len(line) > 0 {
				e.lines <- line
			}
			if err != nil {
				close(e.lines)
				return
			}
		}
	}()
	return e
}

func (e *endpoint) send(v any) {
	e.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.w.Write(append(b, '\n')); err != nil {
		e.t.Fatal(err)
	}
}

func (e *endpoint) next() map[string]any {
	e.t.Helper()
	select {
	case line, ok := <-e.lines:
		if !ok {
			e.t.Fatal("endpoint closed")
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			e.t.Fatalf("decode %q: %v", line, err)
		}
		return m
	case <-time.After(2 * time.Second):
		e.t.Fatal("timed out waiting for a message")
	}
	return nil
}

// nextWhere skips messages until match accepts one.
func (e *endpoint) nextWhere(match func(map[string]any) bool) map[string]any {
	e.t.Helper()
	for {
		if m := e.next(); match(m) {
			return m
		}
	}
}

func (e *endpoint) quiet(d time.Duration) { //nolint:unparam // the wait is a per-call choice
	e.t.Helper()
	select {
	case line := <-e.lines:
		e.t.Fatalf("unexpected message: %s", line)
	case <-time.After(d):
	}
}

func isMethod(method string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["method"] == method }
}

func isEvent(typ string) func(map[string]any) bool {
	return func(m map[string]any) bool { return m["type"] == typ }
}

func stopReason(m map[string]any) string {
	r, _ := m["result"].(map[string]any)
	s, _ := r["stopReason"].(string)
	return s
}

func update(sessionID, text string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "method": "session/update",
		"params": map[string]any{
			"sessionId": sessionID,
			"update": map[string]any{
				"sessionUpdate": "agent_message_chunk",
				"content":       map[string]any{"type": "text", "text": text},
			},
		},
	}
}

func TestTakeoverHoldsDriverTurnAndHandbackEndsIt(t *testing.T) {
	driverInR, driverInW := io.Pipe()
	driverOutR, driverOutW := io.Pipe()
	agentInR, agentInW := io.Pipe()
	agentOutR, agentOutW := io.Pipe()

	sock := filepath.Join(t.TempDir(), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	b := broker.New(broker.Config{DriverIn: driverInR, DriverOut: driverOutW, AgentIn: agentInW, AgentOut: agentOutR})
	go func() { _ = transport.ServeUnix(ln, b) }()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx) }()

	driver := newEndpoint(t, driverOutR, driverInW)
	agent := newEndpoint(t, agentInR, agentOutW)

	// Handshake passes through, and the broker learns the session.
	driver.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	agent.nextWhere(isMethod("initialize"))
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"protocolVersion": 1, "_meta": map[string]any{"steering": map[string]any{"supported": true}}}})
	driver.next()
	driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/new", "params": map[string]any{"cwd": "/w", "mcpServers": []any{}}})
	agent.nextWhere(isMethod("session/new"))
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"sessionId": "s1"}})
	driver.next()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	viewer := newEndpoint(t, conn, conn)
	if st := viewer.nextWhere(isEvent("state")); st["sessionId"] != "s1" || st["steering"] != true {
		t.Fatalf("viewer state = %v", st)
	}

	// The driver starts a turn; both the driver and the viewer see its updates.
	driver.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": "s1", "prompt": []any{}}})
	agent.nextWhere(isMethod("session/prompt"))
	agent.send(update("s1", "working"))
	driver.nextWhere(isMethod("session/update"))
	viewer.nextWhere(isEvent("update"))

	// Takeover cancels the agent's turn but holds the driver's request open.
	viewer.send(broker.Command{Op: "takeover"})
	cancelMsg := agent.nextWhere(isMethod("session/cancel"))
	if p := cancelMsg["params"].(map[string]any); p["sessionId"] != "s1" {
		t.Fatalf("cancel params = %v", p)
	}
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 3, "result": map[string]any{"stopReason": "cancelled"}})
	viewer.nextWhere(func(m map[string]any) bool { return m["type"] == "state" && m["driverTurn"] == "held" })
	driver.quiet(200 * time.Millisecond)

	// The human prompts the same session. The driver still sees the updates,
	// because its turn is held open.
	viewer.send(broker.Command{Op: "prompt", Text: "look at the failing test instead"})
	hp := agent.nextWhere(isMethod("session/prompt"))
	if p := hp["params"].(map[string]any); p["sessionId"] != "s1" {
		t.Fatalf("human prompt went to session %v", p["sessionId"])
	}
	agent.send(update("s1", "ok, switching"))
	driver.nextWhere(isMethod("session/update"))
	agent.send(map[string]any{"jsonrpc": "2.0", "id": hp["id"], "result": map[string]any{"stopReason": "end_turn"}})
	if r := viewer.nextWhere(isEvent("result")); r["op"] != "prompt" {
		t.Fatalf("viewer result = %v", r)
	}
	driver.quiet(200 * time.Millisecond)

	// A driver prompt sent while the human holds the session is queued.
	driver.send(map[string]any{"jsonrpc": "2.0", "id": 4, "method": "session/prompt", "params": map[string]any{"sessionId": "s1", "prompt": []any{}}})
	agent.quiet(200 * time.Millisecond)

	// Handback ends the held driver turn normally, then releases the queued one.
	viewer.send(broker.Command{Op: "handback"})
	held := driver.next()
	if held["id"] != float64(3) || stopReason(held) != "end_turn" {
		t.Fatalf("held turn answered with %v", held)
	}
	if q := agent.nextWhere(isMethod("session/prompt")); q["id"] != float64(4) {
		t.Fatalf("queued prompt = %v", q)
	}
}

// harness starts a broker on pipes and returns the driver and agent ends,
// with a session "s1" already created.
func harness(t *testing.T, cfg broker.Config) (driver, agent *endpoint, sock string) {
	t.Helper()
	driverInR, driverInW := io.Pipe()
	driverOutR, driverOutW := io.Pipe()
	agentInR, agentInW := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	sock = filepath.Join(t.TempDir(), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	cfg.DriverIn, cfg.DriverOut, cfg.AgentIn, cfg.AgentOut = driverInR, driverOutW, agentInW, agentOutR
	b := broker.New(cfg)
	go func() { _ = transport.ServeUnix(ln, b) }()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx) }()
	driver = newEndpoint(t, driverOutR, driverInW)
	agent = newEndpoint(t, agentInR, agentOutW)
	driver.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{}})
	agent.next()
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"sessionId": "s1"}})
	driver.next()
	return driver, agent, sock
}

func attachViewer(t *testing.T, sock string) *endpoint {
	t.Helper()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	v := newEndpoint(t, conn, conn)
	v.nextWhere(isEvent("state"))
	return v
}

func permissionRequest(id string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id, "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "s1",
			"toolCall":  map[string]any{"toolCallId": "t1", "title": "Write progress.txt"},
			"options": []any{
				map[string]any{"optionId": "allow-once", "name": "Yes", "kind": "allow_once"},
				map[string]any{"optionId": "reject", "name": "No", "kind": "reject_once"},
			},
		},
	}
}

func TestViewerAnswersPermission(t *testing.T) {
	driver, agent, sock := harness(t, broker.Config{})
	viewer := attachViewer(t, sock)

	agent.send(permissionRequest("p1"))
	ev := viewer.nextWhere(isEvent("permission"))
	if ev["id"] != `"p1"` {
		t.Fatalf("permission event id = %v", ev["id"])
	}
	driver.quiet(200 * time.Millisecond)

	// Viewers may name a string ID without its JSON quotes, and an option by
	// its kind.
	viewer.send(broker.Command{Op: "permit", ID: "p1", Option: "allow_once"})
	resp := agent.next()
	outcome := resp["result"].(map[string]any)["outcome"].(map[string]any)
	if resp["id"] != "p1" || outcome["outcome"] != "selected" || outcome["optionId"] != "allow-once" {
		t.Fatalf("agent got %v", resp)
	}
	driver.quiet(200 * time.Millisecond)
}

func TestUnansweredPermissionGoesToDriver(t *testing.T) {
	driver, agent, sock := harness(t, broker.Config{AnswerTimeout: 100 * time.Millisecond})
	viewer := attachViewer(t, sock)

	agent.send(permissionRequest("p1"))
	viewer.nextWhere(isEvent("permission"))
	if got := driver.nextWhere(isMethod("session/request_permission")); got["id"] != "p1" {
		t.Fatalf("driver got %v", got)
	}
	viewer.nextWhere(isEvent("permission_resolved"))

	// A late answer finds nothing to answer, and the driver's own answer
	// reaches the agent.
	viewer.send(broker.Command{Op: "permit", ID: "p1", Option: "allow_once"})
	viewer.nextWhere(isEvent("error"))
	driver.send(map[string]any{"jsonrpc": "2.0", "id": "p1", "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
	if got := agent.next(); got["id"] != "p1" {
		t.Fatalf("agent got %v", got)
	}
}

// A request that finds no viewer waits for the answer timeout, escalated
// at once, before the driver decides.
func TestPermissionWithoutViewerGoesToDriver(t *testing.T) {
	driver, agent, _ := harness(t, broker.Config{AnswerTimeout: 100 * time.Millisecond})
	agent.send(permissionRequest("p1"))
	if got := driver.nextWhere(isMethod("session/request_permission")); got["id"] != "p1" {
		t.Fatalf("driver got %v", got)
	}
}

func promptText(m map[string]any) string {
	p, _ := m["params"].(map[string]any)
	blocks, _ := p["prompt"].([]any)
	if len(blocks) == 0 {
		return ""
	}
	b, _ := blocks[0].(map[string]any)
	s, _ := b["text"].(string)
	return s
}

// An agent without native steering gets the steer as a turn of its own in
// the same session once the driver's turn ends, before the driver hears
// that it ended; nothing is cancelled.
func TestSteerWithoutSteeringRunsAfterTheDriverTurn(t *testing.T) {
	driver, agent, sock := harness(t, broker.Config{})
	viewer := attachViewer(t, sock)

	driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	agent.nextWhere(isMethod("session/prompt"))

	viewer.send(broker.Command{Op: "steer", Text: "focus on the parser"})
	agent.quiet(200 * time.Millisecond)
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "end_turn"}})

	steer := agent.nextWhere(isMethod("session/prompt"))
	if promptText(steer) != "focus on the parser" || steer["params"].(map[string]any)["sessionId"] != "s1" {
		t.Fatalf("steer prompt = %v", steer)
	}
	driver.quiet(200 * time.Millisecond)

	agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"stopReason": "end_turn"}})
	if got := driver.next(); got["id"] != float64(2) || stopReason(got) != "end_turn" {
		t.Fatalf("driver turn answered with %v", got)
	}
	viewer.nextWhere(func(m map[string]any) bool {
		return m["type"] == "state" && m["floor"] == "driver" && m["driverTurn"] == "none"
	})
}

func TestSteerWhileIdleQueuesDriverPrompt(t *testing.T) {
	driver, agent, sock := harness(t, broker.Config{})
	viewer := attachViewer(t, sock)

	viewer.send(broker.Command{Op: "steer", Text: "check the logs first"})
	steer := agent.nextWhere(isMethod("session/prompt"))
	if promptText(steer) != "check the logs first" {
		t.Fatalf("steer prompt = %v", steer)
	}

	driver.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	agent.quiet(200 * time.Millisecond)

	agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"stopReason": "end_turn"}})
	if got := agent.nextWhere(isMethod("session/prompt")); got["id"] != float64(3) {
		t.Fatalf("queued driver prompt = %v", got)
	}
}

// The driver's own cancel, sent while a takeover waits for the turn to end,
// must still reach the driver.
func TestDriverCancelDuringTakeoverReachesDriver(t *testing.T) {
	driver, agent, sock := harness(t, broker.Config{})
	viewer := attachViewer(t, sock)

	driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	agent.nextWhere(isMethod("session/prompt"))
	viewer.send(broker.Command{Op: "takeover"})
	agent.nextWhere(isMethod("session/cancel"))

	driver.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": "s1"}})
	agent.nextWhere(isMethod("session/cancel"))
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "cancelled"}})
	if got := driver.next(); got["id"] != float64(2) || stopReason(got) != "cancelled" {
		t.Fatalf("driver got %v", got)
	}
}

func TestDriverCancelEndsHeldTurn(t *testing.T) {
	driverInR, driverInW := io.Pipe()
	driverOutR, driverOutW := io.Pipe()
	agentInR, agentInW := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	sock := filepath.Join(t.TempDir(), "b.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	b := broker.New(broker.Config{DriverIn: driverInR, DriverOut: driverOutW, AgentIn: agentInW, AgentOut: agentOutR})
	go func() { _ = transport.ServeUnix(ln, b) }()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx) }()
	driver := newEndpoint(t, driverOutR, driverInW)
	agent := newEndpoint(t, agentInR, agentOutW)

	driver.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{}})
	agent.next()
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"sessionId": "s1"}})
	driver.next()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	viewer := newEndpoint(t, conn, conn)
	viewer.next()

	driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	agent.next()
	viewer.send(broker.Command{Op: "takeover"})
	agent.nextWhere(isMethod("session/cancel"))
	agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "cancelled"}})
	viewer.nextWhere(func(m map[string]any) bool { return m["driverTurn"] == "held" })

	// The orchestrator gives up on its turn, for example on its own timeout.
	driver.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": "s1"}})
	if got := driver.next(); got["id"] != float64(2) || stopReason(got) != "cancelled" {
		t.Fatalf("held turn answered with %v", got)
	}
	agent.nextWhere(isMethod("session/cancel"))
}
