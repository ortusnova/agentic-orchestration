package broker_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// started is a broker running on pipes, with a driver and an agent end.
type started struct {
	b      *broker.Broker
	driver *endpoint
	agent  *endpoint
	cancel context.CancelFunc
}

// startBroker runs a broker for cfg on pipes and creates session "s1".
func startBroker(t *testing.T, cfg broker.Config) *started {
	t.Helper()
	return startBrokerWrapped(t, cfg, nil)
}

// startBrokerWrapped is startBroker with the broker reading the agent's
// output through wrap, when wrap is non-nil.
func startBrokerWrapped(t *testing.T, cfg broker.Config, wrap func(io.Reader) io.Reader) *started {
	t.Helper()
	driverInR, driverInW := io.Pipe()
	driverOutR, driverOutW := io.Pipe()
	agentInR, agentInW := io.Pipe()
	agentOutR, agentOutW := io.Pipe()
	var agentOut io.Reader = agentOutR
	if wrap != nil {
		agentOut = wrap(agentOutR)
	}
	cfg.DriverIn, cfg.DriverOut, cfg.AgentIn, cfg.AgentOut = driverInR, driverOutW, agentInW, agentOut
	b := broker.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = b.Run(ctx) }()
	s := &started{b: b, driver: newEndpoint(t, driverOutR, driverInW), agent: newEndpoint(t, agentInR, agentOutW), cancel: cancel}
	s.driver.send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "session/new", "params": map[string]any{}})
	s.agent.next()
	s.agent.send(map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"sessionId": "s1"}})
	s.driver.next()
	return s
}

// viewerEvents decodes a viewer's records as they arrive.
type viewerEvents struct {
	t *testing.T
	v *broker.Viewer
}

func (ve viewerEvents) next() broker.Event {
	ve.t.Helper()
	select {
	case rec, ok := <-ve.v.Lines():
		if !ok {
			ve.t.Fatal("viewer disconnected")
		}
		var ev broker.Event
		if err := json.Unmarshal(rec.Data, &ev); err != nil {
			ve.t.Fatalf("decode %q: %v", rec.Data, err)
		}
		if ev.Seq != rec.Seq {
			ve.t.Fatalf("record seq %d carries event seq %d", rec.Seq, ev.Seq)
		}
		return ev
	case <-time.After(2 * time.Second):
		ve.t.Fatal("timed out waiting for a viewer event")
	}
	return broker.Event{}
}

func (ve viewerEvents) nextWhere(match func(broker.Event) bool) broker.Event {
	ve.t.Helper()
	for {
		if ev := ve.next(); match(ev) {
			return ev
		}
	}
}

func eventType(typ string) func(broker.Event) bool {
	return func(ev broker.Event) bool { return ev.Type == typ }
}

func agentText(text string) map[string]any { return update("s1", text) }

// waitState polls the broker until its state satisfies match.
func (s *started) waitState(t *testing.T, match func(broker.Event) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if match(s.b.State()) {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("state = %+v, never matched", s.b.State())
}

func heldTurn(ev broker.Event) bool { return ev.DriverTurn == "held" }
