package broker_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// initialize runs the initialize handshake, advertising native steering
// when steering is set.
func initialize(t *testing.T, s *started, steering bool) {
	t.Helper()
	result := map[string]any{"protocolVersion": 1}
	if steering {
		result["_meta"] = map[string]any{"steering": map[string]any{"supported": true}}
	}
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "initialize", "params": map[string]any{"protocolVersion": 1}})
	s.agent.nextWhere(isMethod("initialize"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 100, "result": result})
	s.driver.next()
}

func startDriverTurn(t *testing.T, s *started, id int) {
	t.Helper()
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	s.agent.nextWhere(isMethod("session/prompt"))
}

// promptTexts returns the text of every block of a prompt or steering
// request.
func promptTexts(m map[string]any) []string {
	p, _ := m["params"].(map[string]any)
	blocks, _ := p["prompt"].([]any)
	var texts []string
	for _, b := range blocks {
		block, _ := b.(map[string]any)
		text, _ := block["text"].(string)
		texts = append(texts, text)
	}
	return texts
}

func endTurn(id any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}}
}

// answerTo2 is the agent's answer to driver prompt 2, marked so a test can
// tell it from an answer the broker made up.
func answerTo2() map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "end_turn", "usage": map[string]any{"totalTokens": 42}}}
}

// isAnswerTo2 reports whether m is answerTo2 as the agent sent it.
func isAnswerTo2(m map[string]any) bool {
	r, _ := m["result"].(map[string]any)
	u, _ := r["usage"].(map[string]any)
	return m["id"] == float64(2) && u["totalTokens"] == float64(42)
}

func TestSteer_NativeInjectsIntoTheRunningTurn(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, true)
	ve := viewerEvents{t, s.b.Attach()}
	startDriverTurn(t, s, 2)

	if reply := s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"}); reply.Type != "state" || reply.Floor != broker.FloorDriver {
		t.Fatalf("steer reply = %+v, want the state with the floor still with the driver", reply)
	}
	steer := s.agent.next()
	params, _ := steer["params"].(map[string]any)
	meta, _ := params["_meta"].(map[string]any)
	steering, _ := meta["steering"].(map[string]any)
	if steer["method"] != "_session/steering" || params["sessionId"] != "s1" || !slices.Equal(promptTexts(steer), []string{"focus on the parser"}) || steering["idleBehavior"] != "promptRequired" {
		t.Fatalf("agent got %v, want _session/steering for s1 with the text and idleBehavior promptRequired", steer)
	}

	// A steer while the first awaits its outcome is queued, not refused, and
	// goes in once the first has landed.
	if reply := s.b.Do(broker.Command{Op: "steer", Text: "add a test"}); reply.Type != "state" || reply.SteersQueued != 2 {
		t.Fatalf("second steer reply = %+v, want it accepted with two steers pending", reply)
	}
	s.agent.quiet(100 * time.Millisecond)
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"outcome": "injected"}})
	if r := ve.nextWhere(eventType("result")); r.Op != "steer" || !strings.Contains(string(r.Result), "injected") {
		t.Errorf("viewer result = %+v, want the agent's steering outcome", r)
	}
	if again := s.agent.next(); again["method"] != "_session/steering" || !slices.Equal(promptTexts(again), []string{"add a test"}) {
		t.Fatalf("agent got %v, want the queued steer injected next", again)
	}
	s.driver.quiet(100 * time.Millisecond) // the driver's turn goes on
}

func TestSteer_WaitsForTheRunningTurnWithoutNativeSteering(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)

	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})
	s.agent.quiet(200 * time.Millisecond) // no cancel and no new prompt while the turn runs

	// The driver's turn ends; the steer runs before the driver hears of it.
	s.agent.send(answerTo2())
	steer := s.agent.nextWhere(isMethod("session/prompt"))
	if !slices.Equal(promptTexts(steer), []string{"focus on the parser"}) || steer["params"].(map[string]any)["sessionId"] != "s1" {
		t.Fatalf("steer prompt = %v", steer)
	}
	s.driver.quiet(100 * time.Millisecond)
	if st := s.b.State(); st.Floor != broker.FloorDriver || st.DriverTurn != "held" {
		t.Errorf("state during the steer turn = %+v, want the driver's floor with its turn held", st)
	}

	s.agent.send(endTurn(steer["id"]))
	if got := s.driver.next(); !isAnswerTo2(got) {
		t.Fatalf("driver got %v, want the agent's own answer to prompt 2", got)
	}
}

func TestSteer_QueuedSteersGoAsOneMessage(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})
	s.b.Do(broker.Command{Op: "steer", Text: "add a test"})

	s.agent.send(answerTo2())
	steer := s.agent.nextWhere(isMethod("session/prompt"))
	if got := promptTexts(steer); !slices.Equal(got, []string{"focus on the parser", "add a test"}) {
		t.Fatalf("steer prompt blocks = %q, want both steers in order", got)
	}
	s.agent.send(endTurn(steer["id"]))
	if got := s.driver.next(); !isAnswerTo2(got) {
		t.Fatalf("driver got %v", got)
	}
	s.agent.quiet(100 * time.Millisecond) // exactly one steer turn
}

func TestSteer_IdleRunsASteerTurnForEveryAgent(t *testing.T) {
	t.Parallel()

	for _, native := range []bool{true, false} {
		s := startBroker(t, broker.Config{})
		initialize(t, s, native)

		s.b.Do(broker.Command{Op: "steer", Text: "check the logs first"})
		steer := s.agent.next()
		if steer["method"] != "session/prompt" || !slices.Equal(promptTexts(steer), []string{"check the logs first"}) {
			t.Fatalf("native %v: agent got %v, want a session/prompt with the steer and no native steer", native, steer)
		}
		s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
		s.agent.quiet(200 * time.Millisecond)
		s.agent.send(endTurn(steer["id"]))
		if got := s.agent.nextWhere(isMethod("session/prompt")); got["id"] != float64(3) {
			t.Fatalf("native %v: queued driver prompt = %v", native, got)
		}
		if st := s.b.State(); st.Floor != broker.FloorDriver {
			t.Errorf("native %v: floor after the steer turn = %q, want driver", native, st.Floor)
		}
	}
}

func TestSteer_PromptRequiredRunsAsATurnBeforeTheDriverGetsControl(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	ve := viewerEvents{t, s.b.Attach()}
	initialize(t, s, true)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})
	steer := s.agent.nextWhere(isMethod("_session/steering"))

	// The turn ends before the agent takes the steer.
	s.agent.send(answerTo2())
	s.driver.quiet(100 * time.Millisecond)
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"outcome": "promptRequired"}})

	turn := s.agent.next()
	if turn["method"] != "session/prompt" || !slices.Equal(promptTexts(turn), []string{"focus on the parser"}) {
		t.Fatalf("agent got %v, want the steer as a session/prompt, never a second native steer", turn)
	}
	s.driver.quiet(100 * time.Millisecond)
	s.agent.send(endTurn(turn["id"]))
	if got := s.driver.next(); !isAnswerTo2(got) {
		t.Fatalf("driver got %v, want its answer once the steer landed", got)
	}
	if r := ve.nextWhere(eventType("result")); r.Op != "steer-turn" {
		t.Errorf("first result viewers saw = %+v, want the steer turn's; promptRequired is not final", r)
	}
}

func TestSteer_AcceptedWhileInterruptedAndKeepsTheFloor(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "interrupt"})
	s.agent.nextWhere(isMethod("session/cancel"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "cancelled"}})
	s.waitState(t, heldTurn)

	if reply := s.b.Do(broker.Command{Op: "steer", Text: "look at the failing test"}); reply.Type != "state" {
		t.Fatalf("steer while interrupted = %+v, want it accepted", reply)
	}
	steer := s.agent.nextWhere(isMethod("session/prompt"))
	if !slices.Equal(promptTexts(steer), []string{"look at the failing test"}) {
		t.Fatalf("steer prompt = %v", steer)
	}
	s.agent.send(endTurn(steer["id"]))
	s.driver.quiet(200 * time.Millisecond)
	if st := s.b.State(); st.Floor != broker.FloorHuman || st.DriverTurn != "held" {
		t.Errorf("state after the steer turn = %+v, want humans on the floor and the driver's turn held", st)
	}
}

func TestSteer_WaitsForAHumanTurnWithoutNativeSteering(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	s.b.Do(broker.Command{Op: "interrupt"})
	s.b.Do(broker.Command{Op: "prompt", Text: "human turn"})
	human := s.agent.nextWhere(isMethod("session/prompt"))

	s.b.Do(broker.Command{Op: "steer", Text: "and add a test"})
	s.agent.quiet(200 * time.Millisecond)
	s.agent.send(endTurn(human["id"]))
	if steer := s.agent.nextWhere(isMethod("session/prompt")); !slices.Equal(promptTexts(steer), []string{"and add a test"}) {
		t.Fatalf("steer after the human turn = %v", steer)
	}
}

func TestSteer_DriverCancelGetsItsAnswerAndTheSteerStillLands(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})

	s.driver.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": "s1"}})
	s.agent.nextWhere(isMethod("session/cancel"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "cancelled"}})
	if got := s.driver.next(); got["id"] != float64(2) || stopReason(got) != "cancelled" {
		t.Fatalf("driver got %v, want its cancelled answer at once", got)
	}
	if steer := s.agent.nextWhere(isMethod("session/prompt")); !slices.Equal(promptTexts(steer), []string{"focus on the parser"}) {
		t.Fatalf("steer after the driver's cancel = %v", steer)
	}
}

func TestSteer_InterruptDuringASteerTurnKeepsTheAnswerUntilHandback(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})
	s.agent.send(answerTo2())
	steer := s.agent.nextWhere(isMethod("session/prompt"))

	s.b.Do(broker.Command{Op: "interrupt"})
	s.agent.nextWhere(isMethod("session/cancel"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"stopReason": "cancelled"}})
	s.driver.quiet(200 * time.Millisecond)

	s.b.Do(broker.Command{Op: "handback"})
	if got := s.driver.next(); !isAnswerTo2(got) {
		t.Fatalf("driver got %v on handback, want the agent's own answer to prompt 2", got)
	}
}

func TestSteer_Rejections(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	if reply := s.b.Do(broker.Command{Op: "steer", Text: "  "}); reply.Type != "error" || !strings.Contains(reply.Message, "needs text") {
		t.Errorf("empty steer = %+v, want a refusal", reply)
	}
}

func TestInterrupt_AlwaysGivesHumansTheFloor(t *testing.T) {
	t.Parallel()

	for _, op := range []string{"interrupt", "takeover", "cancel"} {
		s := startBroker(t, broker.Config{})

		// Idle: the floor moves and nothing is cancelled.
		if reply := s.b.Do(broker.Command{Op: op}); reply.Floor != broker.FloorHuman {
			t.Errorf("%s while idle = %+v, want humans on the floor", op, reply)
		}
		s.agent.quiet(100 * time.Millisecond)

		// During a human turn: the turn is cancelled and humans keep the floor.
		s.b.Do(broker.Command{Op: "prompt", Text: "human turn"})
		human := s.agent.nextWhere(isMethod("session/prompt"))
		s.b.Do(broker.Command{Op: op})
		s.agent.nextWhere(isMethod("session/cancel"))
		s.agent.send(map[string]any{"jsonrpc": "2.0", "id": human["id"], "result": map[string]any{"stopReason": "cancelled"}})
		s.waitState(t, func(ev broker.Event) bool { return ev.Floor == broker.FloorHuman })
		s.driver.quiet(100 * time.Millisecond)
	}
}

func TestSteer_PromptRequiredBeforeTheTurnEndsIsNotSentNativelyAgain(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, true)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})
	steer := s.agent.nextWhere(isMethod("_session/steering"))

	// The agent refuses the steer before the broker reads its answer to
	// prompt 2: the steer waits for the turn instead of bouncing again.
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"outcome": "promptRequired"}})
	s.agent.quiet(200 * time.Millisecond)
	s.agent.send(answerTo2())
	turn := s.agent.next()
	if turn["method"] != "session/prompt" || !slices.Equal(promptTexts(turn), []string{"focus on the parser"}) {
		t.Fatalf("agent got %v, want the steer as a session/prompt", turn)
	}
	s.agent.send(endTurn(turn["id"]))
	if got := s.driver.next(); !isAnswerTo2(got) {
		t.Fatalf("driver got %v", got)
	}
}

func TestSteer_DriverCancelWhileWaitingForSteersGetsItsAnswerAtOnce(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus on the parser"})
	s.agent.send(answerTo2())
	s.agent.nextWhere(isMethod("session/prompt"))

	s.driver.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": "s1"}})
	if got := s.driver.next(); !isAnswerTo2(got) {
		t.Fatalf("driver got %v, want the withheld answer at once", got)
	}
}
