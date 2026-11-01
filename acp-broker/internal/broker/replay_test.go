package broker_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// produceUpdates makes the agent send n updates during a driver turn and
// waits until the driver received them, so the broker has logged them.
func produceUpdates(t *testing.T, s *started, n int) {
	t.Helper()
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "session/prompt", "params": map[string]any{"sessionId": "s1"}})
	s.agent.nextWhere(isMethod("session/prompt"))
	for i := range n {
		s.agent.send(agentText(fmt.Sprintf("chunk %d", i+1)))
		s.driver.nextWhere(isMethod("session/update"))
	}
}

func TestEvents_AreNumberedInOrder(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	ve := viewerEvents{t, s.b.Attach()}
	if st := ve.next(); st.Type != "state" || st.Seq != 0 {
		t.Fatalf("first event = %+v, want the state snapshot with seq 0", st)
	}
	produceUpdates(t, s, 5)

	var prev broker.Event
	for i := range 6 { // a state event for the prompt, then five updates
		ev := ve.next()
		if ev.Seq == 0 || (i > 0 && ev.Seq != prev.Seq+1) {
			t.Fatalf("event %d has seq %d after seq %d, want one more", i, ev.Seq, prev.Seq)
		}
		if ev.Time.IsZero() || (i > 0 && ev.Time.Before(prev.Time)) {
			t.Fatalf("event %d time %v precedes %v", i, ev.Time, prev.Time)
		}
		prev = ev
	}
}

func TestLateViewer_GetsStateThenBufferThenLive(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	produceUpdates(t, s, 300)

	ve := viewerEvents{t, s.b.Attach()}
	if st := ve.next(); st.Type != "state" || st.Seq != 0 || st.DriverTurn != "active" {
		t.Fatalf("first event = %+v, want a state snapshot showing the active turn", st)
	}
	// Event 1 is the state event the prompt caused; events 2 to 301 are
	// the updates.
	for want := int64(1); want <= 301; want++ {
		if ev := ve.next(); ev.Seq != want {
			t.Fatalf("buffered event seq = %d, want %d", ev.Seq, want)
		}
	}
	s.agent.send(agentText("live"))
	if ev := ve.next(); ev.Seq != 302 || !strings.Contains(string(ev.Update), "live") {
		t.Fatalf("live event = %+v, want seq 302 carrying the new update", ev)
	}
}

func TestResume_AfterALastEventID(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	produceUpdates(t, s, 300)

	ve := viewerEvents{t, s.b.Resume(280)}
	if ev := ve.next(); ev.Seq != 281 {
		t.Fatalf("first resumed event = %+v, want seq 281 with no snapshot before it", ev)
	}
}

func TestLateViewer_ToldWhenEventsWereEvicted(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{ReplayEvents: 10})
	produceUpdates(t, s, 30)

	ve := viewerEvents{t, s.b.Attach()}
	if st := ve.nextWhere(eventType("state")); st.Seq != 0 {
		t.Fatalf("snapshot seq = %d, want 0", st.Seq)
	}
	marker := ve.next()
	if marker.Type != "error" || marker.Op != "replay" || marker.Seq != 0 || !strings.Contains(marker.Message, "1 to 21") {
		t.Fatalf("event after the snapshot = %+v, want a note that events 1 to 21 were evicted", marker)
	}
	if ev := ve.next(); ev.Seq != 22 {
		t.Fatalf("first buffered event = %d, want 22", ev.Seq)
	}

	resumed := viewerEvents{t, s.b.Resume(5)}
	if m := resumed.next(); m.Type != "error" || !strings.Contains(m.Message, "6 to 21") {
		t.Fatalf("resume after 5 began with %+v, want a note that events 6 to 21 were evicted", m)
	}
}

func TestSlowViewer_IsDisconnectedWithoutSlowingTheRelay(t *testing.T) {
	t.Parallel()

	s := startBroker(t, broker.Config{})
	slow := s.b.Attach() // never read
	produceUpdates(t, s, 2000)

	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-slow.Lines():
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("a viewer that stopped reading was not disconnected")
		}
	}
}
