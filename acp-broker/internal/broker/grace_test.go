package broker_test

import (
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// takeOver starts a driver turn, takes it over from v, and waits until the
// broker holds the driver's request.
func takeOver(t *testing.T, s *started, ve viewerEvents) {
	t.Helper()
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	s.agent.nextWhere(isMethod("session/prompt"))
	ve.v.Command(broker.Command{Op: "takeover"})
	s.agent.nextWhere(isMethod("session/cancel"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 2, "result": map[string]any{"stopReason": "cancelled"}})
	ve.nextWhere(func(ev broker.Event) bool { return ev.Type == "state" && ev.DriverTurn == "held" })
}

func TestHandbackGrace_HandsBackWhenNoViewerReturns(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{HandbackGrace: 150 * time.Millisecond})
	ve := viewerEvents{t, s.b.Attach()}
	takeOver(t, s, ve)

	left := time.Now()
	ve.v.Close()
	got := s.driver.next()
	if got["id"] != float64(2) || stopReason(got) != "end_turn" {
		t.Fatalf("driver got %v, want end_turn for the held prompt", got)
	}
	if waited := time.Since(left); waited < 150*time.Millisecond {
		t.Errorf("handed back after %v, want no earlier than the 150ms grace", waited)
	}
	if st := s.b.State(); st.Floor != broker.FloorDriver {
		t.Errorf("floor after the grace = %q, want driver", st.Floor)
	}
}

func TestHandbackGrace_ReattachKeepsTheFloor(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{HandbackGrace: 300 * time.Millisecond})
	ve := viewerEvents{t, s.b.Attach()}
	takeOver(t, s, ve)

	ve.v.Close()
	time.Sleep(100 * time.Millisecond)
	back := s.b.Attach()
	t.Cleanup(back.Close)

	s.driver.quiet(500 * time.Millisecond)
	if st := s.b.State(); st.Floor != broker.FloorHuman || st.DriverTurn != "held" {
		t.Errorf("state after reattaching within the grace = %+v, want the floor still held by humans", st)
	}
}

func TestHandbackGrace_WaitsForARunningHumanPrompt(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{HandbackGrace: 100 * time.Millisecond})
	ve := viewerEvents{t, s.b.Attach()}
	takeOver(t, s, ve)
	ve.v.Command(broker.Command{Op: "prompt", Text: "look here"})
	hp := s.agent.nextWhere(isMethod("session/prompt"))

	ve.v.Close()
	s.driver.quiet(300 * time.Millisecond) // a human prompt is still running

	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": hp["id"], "result": map[string]any{"stopReason": "end_turn"}})
	if got := s.driver.next(); got["id"] != float64(2) || stopReason(got) != "end_turn" {
		t.Fatalf("driver got %v, want end_turn once the human prompt ended and the grace passed", got)
	}
}
