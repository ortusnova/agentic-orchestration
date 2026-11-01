package broker_test

import (
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// waitEscalation waits for the first escalation.
func (r *recordingSupervisor) waitEscalation(t *testing.T) escalation {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.escalated(); len(got) > 0 {
			return got[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no escalation")
	return escalation{}
}

func TestPermission_AnsweredByAViewer(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup, EscalateAfter: 300 * time.Millisecond, AnswerTimeout: time.Second})
	ve := viewerEvents{t, s.b.Attach()}
	s.agent.send(permissionRequest("p1"))
	ve.nextWhere(eventType("permission"))

	s.b.Do(broker.Command{Op: "permit", ID: "p1", Option: "allow_once"})
	resp := s.agent.next()
	outcome := resp["result"].(map[string]any)["outcome"].(map[string]any)
	if outcome["outcome"] != "selected" || outcome["optionId"] != "allow-once" {
		t.Fatalf("agent got %v", resp)
	}
	sup.waitHolds(t, broker.HoldPermission, "")
	time.Sleep(400 * time.Millisecond) // past the escalation delay
	if got := sup.escalated(); len(got) != 0 {
		t.Errorf("escalations = %+v, want none once a viewer answered", got)
	}
	s.driver.quiet(100 * time.Millisecond)
}

func TestPermission_EscalatedOnceWhenAViewerDoesNotAnswer(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup, EscalateAfter: 100 * time.Millisecond, PublicURL: "https://viewer.example/LIVE-1"})
	ve := viewerEvents{t, s.b.Attach()}
	start := time.Now()
	s.agent.send(permissionRequest("p1"))
	ve.nextWhere(eventType("permission"))

	got := sup.waitEscalation(t)
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Errorf("escalated after %v, want no earlier than the 100ms delay", waited)
	}
	if got != (escalation{kind: "permission", message: "Write progress.txt", url: "https://viewer.example/LIVE-1"}) {
		t.Errorf("escalation = %+v, want kind permission, the tool title, and the public URL", got)
	}
	sup.waitHolds(t, broker.HoldPermission)
	time.Sleep(200 * time.Millisecond)
	if n := len(sup.escalated()); n != 1 {
		t.Errorf("escalations = %d, want exactly one", n)
	}
}

func TestPermission_NoViewerEscalatesAtOnceAndHolds(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup, EscalateAfter: time.Hour})
	s.agent.send(permissionRequest("p1"))

	if got := sup.waitEscalation(t); got.kind != "permission" {
		t.Errorf("escalation = %+v, want kind permission", got)
	}
	sup.waitHolds(t, broker.HoldPermission)
	s.driver.quiet(100 * time.Millisecond) // the driver does not decide yet
}

func TestPermission_LastViewerLeavingEscalatesAtOnce(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup, EscalateAfter: time.Hour})
	ve := viewerEvents{t, s.b.Attach()}
	s.agent.send(permissionRequest("p1"))
	ve.nextWhere(eventType("permission"))
	ve.v.Close()
	sup.waitEscalation(t)
}

func TestPermission_ForwardedToTheDriverAfterTheAnswerTimeout(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup, AnswerTimeout: 150 * time.Millisecond})
	s.agent.send(permissionRequest("p1"))
	sup.waitEscalation(t)

	got := s.driver.nextWhere(isMethod("session/request_permission"))
	if got["id"] != "p1" {
		t.Fatalf("driver got %v", got)
	}
	sup.waitHolds(t, broker.HoldPermission, "")

	// The driver's answer reaches the agent.
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": "p1", "result": map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}})
	if resp := s.agent.next(); resp["id"] != "p1" {
		t.Fatalf("agent got %v", resp)
	}
}

func TestPermission_PassThroughGoesToTheDriverAtOnce(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup, PassThrough: true})
	s.agent.send(permissionRequest("p1"))
	if got := s.driver.nextWhere(isMethod("session/request_permission")); got["id"] != "p1" {
		t.Fatalf("driver got %v", got)
	}
	if got := sup.escalated(); len(got) != 0 {
		t.Errorf("escalations = %+v, want none from a pass-through broker", got)
	}
}

func TestPermission_QuestionEscalatesWithItsOptions(t *testing.T) {
	t.Parallel()

	sup := &recordingSupervisor{}
	s := startBroker(t, broker.Config{Supervisor: sup})
	s.agent.send(map[string]any{
		"jsonrpc": "2.0", "id": "q1", "method": "session/request_permission",
		"params": map[string]any{
			"sessionId": "s1",
			"toolCall":  map[string]any{"toolCallId": "t2", "title": "Which database should I target?"},
			"options": []any{
				map[string]any{"optionId": "pg", "name": "postgres", "kind": "reject_once"},
				map[string]any{"optionId": "lite", "name": "sqlite", "kind": "reject_once"},
			},
		},
	})

	got := sup.waitEscalation(t)
	if got.kind != "question" || got.message != "Which database should I target? Options: postgres, sqlite" {
		t.Errorf("escalation = %+v, want kind question naming both options", got)
	}
}

func TestPermission_CancelledWithTheTurnThatAsked(t *testing.T) {
	t.Parallel()

	cancels := map[string]func(s *started){
		"interrupt":     func(s *started) { s.b.Do(broker.Command{Op: "interrupt"}) },
		"takeover":      func(s *started) { s.b.Do(broker.Command{Op: "takeover"}) },
		"viewer cancel": func(s *started) { s.b.Do(broker.Command{Op: "cancel"}) },
		"driver cancel": func(s *started) {
			s.driver.send(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": "s1"}})
		},
	}
	for name, cancel := range cancels {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := startBroker(t, broker.Config{})
			ve := viewerEvents{t, s.b.Attach()}
			startDriverTurn(t, s, 2)
			s.agent.send(permissionRequest("p1"))
			ve.nextWhere(eventType("permission"))

			cancel(s)
			resp := s.agent.nextWhere(func(m map[string]any) bool { return m["id"] == "p1" })
			outcome := resp["result"].(map[string]any)["outcome"].(map[string]any)
			if outcome["outcome"] != "cancelled" {
				t.Errorf("agent got %v, want the cancelled outcome", resp)
			}
			if ev := ve.nextWhere(eventType("permission_resolved")); ev.ID != `"p1"` {
				t.Errorf("resolved event = %+v", ev)
			}
			s.agent.nextWhere(isMethod("session/cancel"))
		})
	}
}
