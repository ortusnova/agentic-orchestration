package broker_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/secrets"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry/otlp"
)

// spanLog collects the spans a recorder emits.
type spanLog struct {
	mu    sync.Mutex
	spans []telemetry.Span
}

func (l *spanLog) add(sp telemetry.Span) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spans = append(l.spans, sp)
}

// wait returns the spans once at least n have ended.
func (l *spanLog) wait(t *testing.T, n int) []telemetry.Span {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		l.mu.Lock()
		got := append([]telemetry.Span(nil), l.spans...)
		l.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d spans, want at least %d: %+v", len(got), n, got)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (l *spanLog) turns(t *testing.T, n int) []telemetry.Span {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var turns []telemetry.Span
		for _, sp := range l.wait(t, 0) {
			if sp.Kind == telemetry.KindTurn {
				turns = append(turns, sp)
			}
		}
		if len(turns) >= n {
			return turns
		}
		if time.Now().After(deadline) {
			t.Fatalf("got %d turn spans, want %d", len(turns), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

var testLabels = telemetry.Labels{Issue: "LIVE-1", Rule: "implement", ChainID: "c-42", DispatchID: "D1", Attempt: "2"}

// startRecorded starts a broker whose recorder emits into the returned
// log.
func startRecorded(t *testing.T, opts telemetry.Options) (*started, *spanLog) {
	t.Helper()
	log := &spanLog{}
	opts.Emit = log.add
	s := startBroker(t, broker.Config{Telemetry: telemetry.NewRecorder(opts)})
	return s, log
}

func usageResult(id any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
		"stopReason": "end_turn",
		"usage":      map[string]any{"inputTokens": 4, "outputTokens": 102, "cachedReadTokens": 53643, "cachedWriteTokens": 307, "totalTokens": 54056},
	}}
}

func toolUpdate(kind, id string, fields map[string]any) map[string]any {
	u := map[string]any{"sessionUpdate": kind, "toolCallId": id}
	maps.Copy(u, fields)
	return map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "s1", "update": u}}
}

func TestTelemetry_OneSpanPerTurnWithItsOrigin(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	initialize(t, s, false)

	startDriverTurn(t, s, 2)
	s.agent.send(endTurn(2))
	s.driver.next()

	s.b.Do(broker.Command{Op: "steer", Text: "check the logs"})
	steer := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(endTurn(steer["id"]))

	log.turns(t, 2) // the steer turn has ended
	s.b.Do(broker.Command{Op: "interrupt"})
	s.b.Do(broker.Command{Op: "prompt", Text: "human turn"})
	human := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(endTurn(human["id"]))

	turns := log.turns(t, 3)
	want := []telemetry.Origin{telemetry.OriginDriver, telemetry.OriginSteer, telemetry.OriginHuman}
	for i, sp := range turns {
		if sp.Origin != want[i] || sp.StopReason != "end_turn" || sp.End.Before(sp.Start) {
			t.Errorf("turn %d = origin %q, stop %q, %v..%v; want origin %q ending end_turn", i, sp.Origin, sp.StopReason, sp.Start, sp.End, want[i])
		}
	}
	if turns[1].SteerCount != 1 {
		t.Errorf("steer turn delivered %d steers, want 1", turns[1].SteerCount)
	}
}

func TestTelemetry_QueuedDriverPromptStartsAtHandback(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	s.b.Do(broker.Command{Op: "interrupt"})
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 8, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	s.waitState(t, func(ev broker.Event) bool { return ev.DriverTurn == "none+queued" })
	time.Sleep(50 * time.Millisecond)

	handback := time.Now()
	s.b.Do(broker.Command{Op: "handback"})
	s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(endTurn(8))

	if sp := log.turns(t, 1)[0]; sp.Start.Before(handback) {
		t.Errorf("queued prompt's span started at %v, before the handback at %v", sp.Start, handback)
	}
}

func TestTelemetry_TurnOutcomes(t *testing.T) {
	t.Parallel()

	t.Run("interrupt", func(t *testing.T) {
		t.Parallel()
		s, log := startRecorded(t, telemetry.Options{})
		startDriverTurn(t, s, 7)
		s.b.Do(broker.Command{Op: "interrupt"})
		s.agent.nextWhere(isMethod("session/cancel"))
		s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 7, "result": map[string]any{"stopReason": "cancelled"}})
		if sp := log.turns(t, 1)[0]; sp.StopReason != "cancelled" || sp.CancelledFor != telemetry.CancelledForInterrupt {
			t.Errorf("turn = stop %q, cancelled for %q; want cancelled for an interrupt", sp.StopReason, sp.CancelledFor)
		}
	})
	t.Run("agent error", func(t *testing.T) {
		t.Parallel()
		s, log := startRecorded(t, telemetry.Options{})
		startDriverTurn(t, s, 7)
		s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 7, "error": map[string]any{"code": -32603, "message": "model overloaded"}})
		s.driver.next()
		if sp := log.turns(t, 1)[0]; sp.Error != "model overloaded" || sp.Unfinished {
			t.Errorf("turn = error %q, unfinished %v; want the agent's error", sp.Error, sp.Unfinished)
		}
	})
	t.Run("broker stops", func(t *testing.T) {
		t.Parallel()
		s, log := startRecorded(t, telemetry.Options{})
		startDriverTurn(t, s, 7)
		s.agent.send(toolUpdate("tool_call", "call_2", map[string]any{"title": "Run tests", "kind": "execute", "status": "in_progress"}))
		time.Sleep(20 * time.Millisecond)
		s.cancel()
		spans := log.wait(t, 2)
		for _, sp := range spans {
			if !sp.Unfinished {
				t.Errorf("span %+v ended by the broker stopping, want it marked unfinished", sp)
			}
		}
	})
}

func TestTelemetry_NativeSteerIsAnEventOnTheTurn(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	initialize(t, s, true)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus"})
	steer := s.agent.nextWhere(isMethod("_session/steering"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"outcome": "injected"}})
	time.Sleep(20 * time.Millisecond)
	s.agent.send(endTurn(2))
	s.driver.next()

	turns := log.turns(t, 1)
	time.Sleep(50 * time.Millisecond)
	if again := log.turns(t, 1); len(again) != 1 {
		t.Fatalf("got %d turn spans, want only the driver's", len(again))
	}
	if ev := turns[0].SteerEvents; len(ev) != 1 || ev[0].Outcome != "injected" || ev[0].Count != 1 {
		t.Errorf("steer events = %+v, want one injected steer", ev)
	}
}

func TestTelemetry_Usage(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	startDriverTurn(t, s, 2)
	s.agent.send(usageResult(2))
	s.driver.next()
	startDriverTurn(t, s, 3)
	s.agent.send(endTurn(3))
	s.driver.next()
	startDriverTurn(t, s, 4)
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 4, "result": map[string]any{"stopReason": "end_turn", "usage": map[string]any{}}})
	s.driver.next()

	turns := log.turns(t, 3)
	u := turns[0].Usage
	if u == nil || *u.Input != 4 || *u.Output != 102 || *u.CachedRead != 53643 || *u.CachedWrite != 307 || *u.Total != 54056 || u.Reasoning != nil {
		t.Errorf("usage = %+v, want the five reported counts and no reasoning", u)
	}
	if turns[1].Usage != nil || turns[2].Usage != nil {
		t.Errorf("usage of turns reported without counts = %+v, %+v; want none", turns[1].Usage, turns[2].Usage)
	}
}

func TestTelemetry_ModelAtTurnStart(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	ve := viewerEvents{t, s.b.Attach()}
	setModel := func(model string) {
		s.agent.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "s1", "update": map[string]any{"sessionUpdate": "config_option_update", "configOptions": configOptions(model)},
		}})
		// The update reaches the broker on the agent's stream and the next
		// prompt on the driver's, in either order. The broker broadcasts
		// the update after it notes the model, so wait for that broadcast
		// before the turn starts.
		ve.nextWhere(func(e broker.Event) bool { return e.Type == "update" })
	}
	setModel("anthropic/claude-haiku-5-5")
	startDriverTurn(t, s, 2)
	s.agent.send(endTurn(2))
	s.driver.next()
	setModel("anthropic/claude-sonnet-5")
	startDriverTurn(t, s, 3)
	s.agent.send(endTurn(3))
	s.driver.next()

	turns := log.turns(t, 2)
	if turns[0].Model != "anthropic/claude-haiku-5-5" || turns[1].Model != "anthropic/claude-sonnet-5" {
		t.Errorf("models = %q, %q; want each turn's model at its start", turns[0].Model, turns[1].Model)
	}
}

func TestTelemetry_ToolCallsAreChildSpans(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	startDriverTurn(t, s, 2)
	s.agent.send(toolUpdate("tool_call", "call_1", map[string]any{"title": "Read main.go", "kind": "read", "status": "pending"}))
	s.agent.send(toolUpdate("tool_call_update", "call_1", map[string]any{"status": "completed"}))
	s.agent.send(toolUpdate("tool_call", "call_2", map[string]any{"title": "Run tests", "kind": "execute", "status": "in_progress"}))
	time.Sleep(20 * time.Millisecond)
	s.agent.send(endTurn(2))
	s.driver.next()

	spans := log.wait(t, 3)
	turn := log.turns(t, 1)[0]
	byID := map[string]telemetry.Span{}
	for _, sp := range spans {
		if sp.Kind == telemetry.KindTool {
			byID[sp.CallID] = sp
			if sp.TraceID != turn.TraceID || sp.ParentID != turn.SpanID {
				t.Errorf("tool %s is not a child of its turn", sp.CallID)
			}
		}
	}
	if c := byID["call_1"]; c.ToolKind != "read" || c.Title != "Read main.go" || c.Status != "completed" || c.Unfinished {
		t.Errorf("call_1 = %+v, want a completed read titled Read main.go", c)
	}
	if c := byID["call_2"]; !c.Unfinished {
		t.Errorf("call_2 = %+v, want it ended unfinished with its turn", c)
	}
}

func TestTelemetry_ToolCallsPastTheCapAreCounted(t *testing.T) {
	t.Parallel()

	// A human turn, so the many updates are not also relayed to the driver.
	s, log := startRecorded(t, telemetry.Options{})
	s.b.Do(broker.Command{Op: "interrupt"})
	s.b.Do(broker.Command{Op: "prompt", Text: "busy turn"})
	human := s.agent.nextWhere(isMethod("session/prompt"))
	for i := range telemetry.MaxToolsPerTurn + 3 {
		s.agent.send(toolUpdate("tool_call", fmt.Sprintf("call_%d", i), map[string]any{"title": "t", "kind": "read", "status": "completed"}))
	}
	s.agent.send(endTurn(human["id"]))

	turn := log.turns(t, 1)[0]
	if tools := len(log.wait(t, telemetry.MaxToolsPerTurn+1)) - 1; tools != telemetry.MaxToolsPerTurn || turn.ToolsDropped != 3 {
		t.Errorf("recorded %d tool spans and dropped %d, want %d and 3", tools, turn.ToolsDropped, telemetry.MaxToolsPerTurn)
	}
}

func TestTelemetry_UpdatesOutsideATurnAreCountedNotRecorded(t *testing.T) {
	t.Parallel()

	rec := telemetry.NewRecorder(telemetry.Options{})
	// The agent's output reaches the broker late, as on a loaded host: a
	// pipe write returns once the broker's reader has the bytes, before the
	// broker's loop has seen them.
	s := startBrokerWrapped(t, broker.Config{Telemetry: rec}, func(r io.Reader) io.Reader {
		return lateReader{r: r, marker: "late", delay: 50 * time.Millisecond}
	})
	ve := viewerEvents{t, s.b.Attach()}
	s.agent.send(toolUpdate("tool_call", "late", map[string]any{"title": "late", "kind": "read", "status": "completed"}))
	s.agent.send(agentText("late text"))
	// The broker relays an update to viewers once it has recorded it, so
	// both are recorded outside a turn before the driver starts one.
	for range 2 {
		ve.nextWhere(func(e broker.Event) bool { return e.Type == "update" })
	}
	// A turn afterwards is unaffected by them.
	startDriverTurn(t, s, 2)
	s.agent.send(endTurn(2))
	s.driver.next()
	s.cancel()
	// The recorder belongs to the broker's goroutine until Run returns.
	select {
	case <-s.b.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("broker did not stop")
	}
	if got := rec.OutsideTurn(); got != 2 {
		t.Errorf("updates outside a turn = %d, want 2", got)
	}
}

// lateReader delays handing over a read that holds marker, after the
// underlying read returned.
type lateReader struct {
	r      io.Reader
	marker string
	delay  time.Duration
}

func (l lateReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if strings.Contains(string(p[:n]), l.marker) {
		time.Sleep(l.delay)
	}
	return n, err
}

func TestTelemetry_SessionLabelsOnEverySpan(t *testing.T) {
	t.Parallel()

	for _, labels := range []telemetry.Labels{testLabels, {}} {
		s, log := startRecorded(t, telemetry.Options{Labels: labels})
		s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 90, "method": "initialize", "params": map[string]any{}})
		s.agent.nextWhere(isMethod("initialize"))
		s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 90, "result": map[string]any{"agentInfo": map[string]any{"name": "opencode", "version": "1.18.29"}}})
		s.driver.next()
		startDriverTurn(t, s, 2)
		s.agent.send(toolUpdate("tool_call", "call_1", map[string]any{"title": "Read", "kind": "read", "status": "completed"}))
		s.agent.send(endTurn(2))
		s.driver.next()

		for _, sp := range log.wait(t, 2) {
			want := telemetry.Session{ID: "s1", AgentName: "opencode", AgentVersion: "1.18.29", Labels: labels}
			if sp.Session != want {
				t.Errorf("span session = %+v, want %+v", sp.Session, want)
			}
		}
	}
}

func TestTelemetry_ContentOnlyWhenEnabled(t *testing.T) {
	t.Parallel()

	for _, content := range []bool{false, true} {
		s, log := startRecorded(t, telemetry.Options{Content: content})
		s.b.Do(broker.Command{Op: "interrupt"})
		s.b.Do(broker.Command{Op: "prompt", Text: "check the logs"})
		human := s.agent.nextWhere(isMethod("session/prompt"))
		s.agent.send(toolUpdate("tool_call", "call_1", map[string]any{"title": "Read", "kind": "read", "status": "pending", "rawInput": map[string]any{"path": "app.log"}}))
		s.agent.send(toolUpdate("tool_call_update", "call_1", map[string]any{"status": "completed", "rawOutput": map[string]any{"text": "all good"}}))
		s.agent.send(agentText("The logs look fine."))
		time.Sleep(20 * time.Millisecond)
		s.agent.send(endTurn(human["id"]))

		spans := log.wait(t, 2)
		tool, turn := spans[0], spans[1]
		if content {
			if turn.Input != "check the logs" || turn.Output != "The logs look fine." || tool.Input != `{"path":"app.log"}` || tool.Output != `{"text":"all good"}` {
				t.Errorf("content = turn %q/%q, tool %q/%q", turn.Input, turn.Output, tool.Input, tool.Output)
			}
		} else if turn.Input != "" || turn.Output != "" || tool.Input != "" || tool.Output != "" {
			t.Errorf("content recorded although disabled: turn %q/%q, tool %q/%q", turn.Input, turn.Output, tool.Input, tool.Output)
		}
	}
}

func TestTelemetry_BlockedExporterDoesNotSlowTheRelay(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); hanging.Close() })

	exp := otlp.New(otlp.Config{Endpoint: hanging.URL, BufferSize: 8, BatchSize: 4, BatchInterval: time.Millisecond, Logger: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go exp.Run(ctx)
	s := startBroker(t, broker.Config{Telemetry: telemetry.NewRecorder(telemetry.Options{Emit: exp.Enqueue})})

	var slowest time.Duration
	for id := 2; id < 102; id++ {
		start := time.Now()
		startDriverTurn(t, s, id)
		s.agent.send(toolUpdate("tool_call", "c", map[string]any{"title": "Read", "kind": "read", "status": "completed"}))
		s.driver.nextWhere(isMethod("session/update"))
		s.agent.send(endTurn(id))
		s.driver.next()
		slowest = max(slowest, time.Since(start))
	}
	if slowest > 250*time.Millisecond {
		t.Errorf("slowest turn round trip = %v while the exporter is blocked, want the relay unaffected", slowest)
	}
	if exp.Dropped() == 0 {
		t.Errorf("dropped = 0, want the full buffer to have dropped spans")
	}
}

// encodeAll returns the export request for the spans a recorder emitted.
func encodeAll(t *testing.T, spans []telemetry.Span) string {
	t.Helper()
	raw, err := otlp.Encoder{}.Encode(spans)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestTelemetry_DefaultExportCarriesNoTranscript(t *testing.T) {
	t.Parallel()

	s, log := startRecorded(t, telemetry.Options{})
	s.b.Do(broker.Command{Op: "interrupt"})
	s.b.Do(broker.Command{Op: "prompt", Text: "transcript-prompt-text"})
	human := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(toolUpdate("tool_call", "call_1", map[string]any{"title": "Read", "kind": "read", "status": "pending", "rawInput": map[string]any{"path": "transcript-tool-input"}}))
	s.agent.send(toolUpdate("tool_call_update", "call_1", map[string]any{"status": "completed", "rawOutput": map[string]any{"text": "transcript-tool-output"}}))
	s.agent.send(agentText("transcript-agent-text"))
	time.Sleep(20 * time.Millisecond)
	s.agent.send(usageResult(human["id"]))

	out := encodeAll(t, log.wait(t, 2))
	for _, leaked := range []string{"transcript-prompt-text", "transcript-tool-input", "transcript-tool-output", "transcript-agent-text"} {
		if strings.Contains(out, leaked) {
			t.Errorf("default export carries %q: %s", leaked, out)
		}
	}
	for _, kept := range []string{`"turn human"`, `"tool read"`, `"acp.tool.title"`, `"langfuse.observation.usage_details"`} {
		if !strings.Contains(out, kept) {
			t.Errorf("default export lacks %s", kept)
		}
	}
}

func TestTelemetry_ViewersSeeWhatTelemetryMasks(t *testing.T) {
	t.Parallel()

	secret := "viewerTestSecret-5e4d3c2b"
	secrets.Add("test", secret)
	s, log := startRecorded(t, telemetry.Options{Content: true})
	ve := viewerEvents{t, s.b.Attach()}
	s.b.Do(broker.Command{Op: "interrupt"})
	s.b.Do(broker.Command{Op: "prompt", Text: "check the logs"})
	human := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(agentText("the key is " + secret))
	ev := ve.nextWhere(eventType("update"))
	s.agent.send(endTurn(human["id"]))

	if !strings.Contains(string(ev.Update), secret) {
		t.Errorf("viewer update = %s, want the message unchanged", ev.Update)
	}
	out := encodeAll(t, log.wait(t, 1))
	if strings.Contains(out, secret) || !strings.Contains(out, "the key is "+secrets.Marker) || !strings.Contains(out, "check the logs") {
		t.Errorf("export = %s, want the prompt as input and the secret masked in the output", out)
	}
}
