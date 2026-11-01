package broker_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// recordingSupervisor records what the broker asks of its supervisor.
type recordingSupervisor struct {
	mu          sync.Mutex
	holds       []string
	escalations []escalation
}

type escalation struct{ kind, message, url string }

func (r *recordingSupervisor) SetHold(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.holds = append(r.holds, reason)
}

func (r *recordingSupervisor) Escalate(kind, message, url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.escalations = append(r.escalations, escalation{kind, message, url})
}

func (r *recordingSupervisor) holdSequence() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.holds)
}

func (r *recordingSupervisor) escalated() []escalation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.escalations)
}

func (r *recordingSupervisor) waitHolds(t *testing.T, want ...string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if slices.Equal(r.holdSequence(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("hold sequence = %q, want %q", r.holdSequence(), want)
}

func TestHold_TakeoverHoldsAndHandbackReleases(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	ve := viewerEvents{t, s.b.Attach()}
	takeOver(t, s, ve)
	sup.waitHolds(t, broker.HoldInterrupt)

	s.b.Do(broker.Command{Op: "handback"})
	s.driver.next()
	sup.waitHolds(t, broker.HoldInterrupt, "")
}

func TestHold_QueuedSteerHoldsWhileTheDriverWaits(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	initialize(t, s, false)
	startDriverTurn(t, s, 2)

	// Queuing the steer holds nothing: the driver's turn goes on.
	s.b.Do(broker.Command{Op: "steer", Text: "focus"})
	time.Sleep(50 * time.Millisecond)
	if got := sup.holdSequence(); len(got) != 0 {
		t.Fatalf("hold sequence = %q, want none while the steer waits for the turn", got)
	}
	s.agent.send(endTurn(2))
	sup.waitHolds(t, broker.HoldSteer)
	steer := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(endTurn(steer["id"]))
	s.driver.next()
	sup.waitHolds(t, broker.HoldSteer, "")
}

func TestHold_NativeSteerHoldsNothing(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	initialize(t, s, true)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus"})
	s.agent.nextWhere(isMethod("_session/steering"))
	time.Sleep(50 * time.Millisecond)
	if got := sup.holdSequence(); len(got) != 0 {
		t.Errorf("hold sequence = %q, want none for a native steer", got)
	}
}

func TestHold_WaitingPermissionHoldsUntilAnswered(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	ve := viewerEvents{t, s.b.Attach()}
	s.agent.send(permissionRequest("p1"))
	ve.nextWhere(eventType("permission"))
	sup.waitHolds(t, broker.HoldPermission)

	s.b.Do(broker.Command{Op: "permit", ID: "p1", Option: "allow_once"})
	s.agent.next()
	sup.waitHolds(t, broker.HoldPermission, "")
	if got := sup.escalated(); len(got) != 0 {
		t.Errorf("escalations = %+v, want none for a request a viewer answered at once", got)
	}
}

func TestHold_TakeoverDuringAPermissionKeepsOneHold(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	ve := viewerEvents{t, s.b.Attach()}
	startDriverTurn(t, s, 7)
	s.agent.send(permissionRequest("p7"))
	ve.nextWhere(eventType("permission"))
	sup.waitHolds(t, broker.HoldPermission)

	// The takeover cancels the waiting request, and the run stays held
	// without a second hold call.
	s.b.Do(broker.Command{Op: "takeover"})
	s.agent.nextWhere(func(m map[string]any) bool { return m["id"] == "p7" })
	s.agent.nextWhere(isMethod("session/cancel"))
	time.Sleep(50 * time.Millisecond)
	sup.waitHolds(t, broker.HoldPermission)
}

func TestHold_NativeSteerWhileIdleHoldsUntilItsTurnEnds(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	initialize(t, s, true)

	s.b.Do(broker.Command{Op: "steer", Text: "focus"})
	sup.waitHolds(t, broker.HoldSteer)
	steer := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"stopReason": "end_turn"}})
	sup.waitHolds(t, broker.HoldSteer, "")
}

func TestHold_PromptRequiredHoldsUntilTheSteerTurnEnds(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	initialize(t, s, true)
	startDriverTurn(t, s, 2)
	s.b.Do(broker.Command{Op: "steer", Text: "focus"})
	steer := s.agent.nextWhere(isMethod("_session/steering"))

	// The driver's turn ends with the native steer in flight: the driver
	// waits for it, so the run is held.
	s.agent.send(endTurn(2))
	sup.waitHolds(t, broker.HoldSteer)
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": steer["id"], "result": map[string]any{"outcome": "promptRequired"}})
	turn := s.agent.nextWhere(isMethod("session/prompt"))
	s.agent.send(endTurn(turn["id"]))
	s.driver.next()
	sup.waitHolds(t, broker.HoldSteer, "")
}
