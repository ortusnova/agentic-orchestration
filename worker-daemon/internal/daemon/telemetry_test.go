package daemon_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/daemon"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/fakecp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/machine/fakemachine"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp/otlptest"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

// telemetryJob offers ONS-7 with a telemetry flow; its agent works and
// then waits for its input, which the test closes with closeInput.
func (h *harness) telemetryJob(tel *protocol.RunTelemetry) {
	h.offerJob("ONS-7", 1, map[string]string{"AGENT_MODE": "wait-input", "SORTIE_DISPATCH_ID": "D-from-env"}, func(j *fakecp.Job) {
		j.TelemetryFlow = true
		j.Run.Telemetry = tel
		j.KeepInputOpen = true
	})
	h.waitFor("the agent to wait for its input", 20*time.Second, func() bool {
		return strings.Contains(h.fake.StdioOutput("ONS-7", 1), "waiting for input")
	})
}

// receiver is the guest address of the job's OTLP receiver.
func (h *harness) receiver() string {
	h.t.Helper()
	addr := h.machine("ONS-7", 1).GuestAddr(protocol.FlowTelemetry)
	if addr == "" {
		h.t.Fatal("the machine has no telemetry forward")
	}
	return addr
}

// send posts one export request to the receiver as the guest's SDK
// does, with no credential, and returns how long the answer took.
func (h *harness) send(sig, enc string, body []byte) time.Duration {
	h.t.Helper()
	start := time.Now()
	resp, err := http.Post("http://"+h.receiver()+"/v1/"+sig, enc, bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("POST /v1/%s: %s: %s", sig, resp.Status, b)
	}
	return time.Since(start)
}

func (h *harness) closeInput() {
	h.t.Helper()
	if err := h.fake.CloseInput("ONS-7", 1); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) report() protocol.Report {
	h.t.Helper()
	h.waitFor("the report", 30*time.Second, func() bool { return len(h.reports("ONS-7", 1)) > 0 })
	return h.reports("ONS-7", 1)[0]
}

func spanName(i int) string { return fmt.Sprintf("span-%02d", i) }

// shippedNames are the item names of the requests shipped, in order.
func shippedNames(t *testing.T, reqs []fakecp.TelemetryRequest) []string {
	t.Helper()
	var names []string
	for _, r := range reqs {
		rs, err := otlp.Inspect(r.Signal, r.ContentType, r.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, res := range rs {
			for _, s := range res.Strings {
				if strings.HasPrefix(s, "span-") {
					names = append(names, s)
				}
			}
		}
	}
	return names
}

// Each signal, in protobuf and JSON, reaches the telemetry flow with the
// attempt's sortie.* attributes on its resource, over what the guest
// claimed; the agent's environment points its SDK at the receiver.
func TestTelemetryEachSignalIsStampedAndShipped(t *testing.T) {
	h := newHarness(t, defaults())
	h.telemetryJob(&protocol.RunTelemetry{Traceparent: testTraceparent, DispatchID: "D1"})
	claimed := []otlp.Attr{{Key: "service.name", Value: "opencode"}, {Key: "sortie.attempt", Value: "9"}, {Key: "sortie.dispatch.id", Value: "forged"}}
	var sent []string
	for _, sig := range otlp.Signals {
		h.send(sig, otlp.ContentTypeProtobuf, otlptest.Protobuf(sig, claimed, "pb-"+sig, nil))
		h.send(sig, otlp.ContentTypeJSON, otlptest.JSON(sig, claimed, "json-"+sig, nil))
		sent = append(sent, sig+" "+otlp.ContentTypeProtobuf, sig+" "+otlp.ContentTypeJSON)
	}
	h.closeInput()
	r := h.report()
	if r.Outcome != protocol.OutcomeSucceeded {
		t.Fatalf("report %+v", r)
	}
	want := protocol.TelemetryCounts{Received: 6, Shipped: 6}
	if r.Telemetry == nil || *r.Telemetry != want {
		t.Fatalf("report telemetry %+v, want %+v", r.Telemetry, want)
	}
	reqs := h.fake.Telemetry("ONS-7", 1)
	var got []string
	for _, req := range reqs {
		got = append(got, req.Signal+" "+req.ContentType)
		rs, err := otlp.Inspect(req.Signal, req.ContentType, req.Body)
		if err != nil || len(rs) != 1 {
			t.Fatalf("%s %s: %v, %d resources", req.Signal, req.ContentType, err, len(rs))
		}
		for k, v := range map[string]string{"service.name": "opencode", "sortie.issue.identifier": "ONS-7", "sortie.dispatch.id": "D1", "sortie.attempt": "1"} {
			if rs[0].Attributes[k] != v {
				t.Errorf("%s %s: resource %s = %q, want %q", req.Signal, req.ContentType, k, rs[0].Attributes[k], v)
			}
		}
	}
	if !slices.Equal(got, sent) {
		t.Errorf("shipped %q, want %q", got, sent)
	}

	// The receiver is a forward on guest port 4318, on loopback.
	m := h.machine("ONS-7", 1)
	var fw *machine.Forward
	for _, f := range m.Spec().Forwards {
		if f.Name == protocol.FlowTelemetry {
			fw = &f
		}
	}
	if fw == nil || fw.GuestPort != 4318 {
		t.Fatalf("telemetry forward %+v", fw)
	}
	if argv := strings.Join(machine.ForwardArgv(*fw), " "); !strings.Contains(argv, "TCP-LISTEN:4318,bind=127.0.0.1") {
		t.Errorf("forward argv %q", argv)
	}
	env := agentEnv(t, m)
	for k, v := range map[string]string{
		daemon.OTLPEndpointEnv:      "http://" + m.GuestAddr(protocol.FlowTelemetry),
		daemon.OTLPProtocolEnv:      "http/protobuf",
		daemon.OTLPResourceAttrsEnv: "sortie.issue.identifier=ONS-7,sortie.dispatch.id=D1,sortie.attempt=1",
		daemon.TraceparentEnv:       testTraceparent,
	} {
		if env[k] != v {
			t.Errorf("agent env %s = %q, want %q", k, env[k], v)
		}
	}
}

// agentEnv is the environment the agent was started with.
func agentEnv(t *testing.T, m *fakemachine.Machine) map[string]string {
	t.Helper()
	for _, e := range m.Execs() {
		if len(e.Argv) > 1 && e.Argv[1] == "fake-agent" {
			env := map[string]string{}
			for _, kv := range e.Env {
				k, v, _ := strings.Cut(kv, "=")
				env[k] = v
			}
			return env
		}
	}
	t.Fatal("the agent never started")
	return nil
}

// A job token the agent put in a span, its resource or a log record is
// masked before it leaves the daemon; so is every other flow token.
func TestTelemetryMasksTheJobsTokens(t *testing.T) {
	h := newHarness(t, defaults())
	h.telemetryJob(nil)
	tokens := h.fake.Tokens("ONS-7", 1)
	if len(tokens) != len(protocol.Flows)+1 {
		t.Fatalf("tokens %d", len(tokens))
	}
	var attrs []otlp.Attr
	for flow, tok := range tokens {
		attrs = append(attrs, otlp.Attr{Key: "leak." + flow, Value: "Authorization: Bearer " + tok})
	}
	tel := tokens[protocol.FlowTelemetry]
	h.send(otlp.Traces, otlp.ContentTypeProtobuf, otlptest.Protobuf(otlp.Traces, []otlp.Attr{{Key: "host", Value: tel}}, "tool "+tel, attrs))
	h.send(otlp.Logs, otlp.ContentTypeJSON, otlptest.JSON(otlp.Logs, nil, "log "+tokens[protocol.FlowEgress], attrs))
	h.closeInput()
	if r := h.report(); r.Telemetry == nil || r.Telemetry.Shipped != 2 {
		t.Fatalf("report %+v", r)
	}
	reqs := h.fake.Telemetry("ONS-7", 1)
	if len(reqs) != 2 {
		t.Fatalf("shipped %d requests", len(reqs))
	}
	for _, req := range reqs {
		for flow, tok := range tokens {
			if bytes.Contains(req.Body, []byte(tok)) {
				t.Errorf("the %s token left in a %s request", flow, req.Signal)
			}
		}
		rs, _ := otlp.Inspect(req.Signal, req.ContentType, req.Body)
		all := strings.Join(rs[0].Strings, "\n")
		if !strings.Contains(all, "Authorization: Bearer [redacted]") {
			t.Errorf("%s: no masked attribute in %q", req.Signal, all)
		}
	}
	rs, _ := otlp.Inspect(reqs[0].Signal, reqs[0].ContentType, reqs[0].Body)
	if rs[0].Attributes["host"] != "[redacted]" || !slices.Contains(rs[0].Strings, "tool [redacted]") {
		t.Errorf("span not masked: %+v", rs[0])
	}
}

// Scenario: The control plane is down for a minute. The flow refuses;
// the receiver still answers at once, the spool keeps what it took, and
// it ships, in order, once the flow answers.
func TestTelemetryARefusingFlowThenARecoveringOne(t *testing.T) {
	h := newHarness(t, defaults())
	h.telemetryJob(nil)
	h.fake.RefuseTelemetry("ONS-7", 1, http.StatusServiceUnavailable)
	for i := range 3 {
		if took := h.send(otlp.Traces, otlp.ContentTypeProtobuf, otlptest.Protobuf(otlp.Traces, nil, spanName(i), nil)); took > time.Second {
			t.Errorf("the receiver took %s to answer while the flow refused", took)
		}
	}
	refused := func() int {
		n := 0
		for _, e := range h.fake.Events() {
			if e.Kind == "telemetry" && e.Status == http.StatusServiceUnavailable {
				n++
			}
		}
		return n
	}
	h.waitFor("the flow to refuse more than once", 10*time.Second, func() bool { return refused() >= 3 })
	if n := len(h.fake.Telemetry("ONS-7", 1)); n != 0 {
		t.Fatalf("a refusing flow took %d requests", n)
	}
	h.fake.RefuseTelemetry("ONS-7", 1, 0)
	h.waitFor("the spool to ship", 10*time.Second, func() bool { return len(h.fake.Telemetry("ONS-7", 1)) == 3 })
	if names := shippedNames(t, h.fake.Telemetry("ONS-7", 1)); !slices.Equal(names, []string{spanName(0), spanName(1), spanName(2)}) {
		t.Errorf("shipped %q", names)
	}
	h.closeInput()
	r := h.report()
	if want := (protocol.TelemetryCounts{Received: 3, Shipped: 3}); r.Outcome != protocol.OutcomeSucceeded || r.Telemetry == nil || *r.Telemetry != want {
		t.Fatalf("report %+v %+v", r, r.Telemetry)
	}
}

// Scenario: Spool full. More than the spool's bound arrives before the
// flow drains it: the oldest are dropped, the report counts them, and
// the run goes on.
func TestTelemetryAFullSpoolDropsTheOldest(t *testing.T) {
	h := newHarness(t, defaults())
	h.telemetryJob(&protocol.RunTelemetry{SpoolBytes: 1600})
	h.fake.RefuseTelemetry("ONS-7", 1, http.StatusServiceUnavailable)
	pad := []otlp.Attr{{Key: "pad", Value: strings.Repeat("x", 400)}}
	const sent = 10
	for i := range sent {
		h.send(otlp.Traces, otlp.ContentTypeProtobuf, otlptest.Protobuf(otlp.Traces, nil, spanName(i), pad))
	}
	// One request over the whole bound is refused and counted.
	resp, err := http.Post("http://"+h.receiver()+"/v1/logs", otlp.ContentTypeJSON, strings.NewReader(strings.Repeat(" ", 2000)+"{}"))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("a request over the bound: %s", resp.Status)
	}
	h.fake.RefuseTelemetry("ONS-7", 1, 0)
	h.closeInput()
	r := h.report()
	if r.Outcome != protocol.OutcomeSucceeded || r.Telemetry == nil {
		t.Fatalf("report %+v", r)
	}
	names := shippedNames(t, h.fake.Telemetry("ONS-7", 1))
	var want []string
	for i := sent - len(names); i < sent; i++ {
		want = append(want, spanName(i))
	}
	if len(names) == 0 || len(names) > 4 || !slices.Equal(names, want) {
		t.Fatalf("shipped %q: want the newest few, %q", names, want)
	}
	c := *r.Telemetry
	if c.Received != sent+1 || c.Shipped != int64(len(names)) || c.Dropped != sent+1-c.Shipped || c.DroppedBytes < 2000 {
		t.Errorf("report telemetry %+v", c)
	}
}

// The spool ships before the result: the daemon holds the result while
// the flow refuses, within the bound, and returns it once the spool is
// shipped.
func TestTelemetryIsFlushedBeforeTheResult(t *testing.T) {
	h := newHarness(t, defaults())
	h.telemetryJob(nil)
	h.fake.RefuseTelemetry("ONS-7", 1, http.StatusServiceUnavailable)
	h.send(otlp.Traces, otlp.ContentTypeProtobuf, otlptest.Protobuf(otlp.Traces, nil, spanName(0), nil))
	h.send(otlp.Metrics, otlp.ContentTypeJSON, otlptest.JSON(otlp.Metrics, nil, spanName(1), nil))
	h.closeInput()
	h.waitFor("the agent's exit", 20*time.Second, func() bool { _, ok := h.fake.Exit("ONS-7", 1); return ok })
	time.Sleep(500 * time.Millisecond)
	if _, ok := h.fake.Result("ONS-7", 1); ok {
		t.Fatal("the result went before the telemetry")
	}
	h.fake.RefuseTelemetry("ONS-7", 1, 0)
	r := h.report()
	if want := (protocol.TelemetryCounts{Received: 2, Shipped: 2}); r.Telemetry == nil || *r.Telemetry != want {
		t.Fatalf("report telemetry %+v", r.Telemetry)
	}
	var lastTelemetry, result time.Time
	for _, e := range h.fake.Events() {
		switch {
		case e.Kind == "telemetry" && e.Status == http.StatusOK:
			lastTelemetry = e.At
		case e.Kind == "result" && e.Status == http.StatusNoContent:
			result = e.At
		}
	}
	if result.IsZero() || !lastTelemetry.Before(result) {
		t.Errorf("telemetry shipped at %v, result at %v", lastTelemetry, result)
	}
}

// A flow that refuses throughout holds the result no longer than the
// flush bound; what it never took is dropped and counted.
func TestTelemetryARefusingFlowHoldsTheResultOnlyForTheBound(t *testing.T) {
	s := defaults()
	s.telemetryFlush = time.Second
	h := newHarness(t, s)
	h.telemetryJob(nil)
	h.fake.RefuseTelemetry("ONS-7", 1, http.StatusBadGateway)
	h.send(otlp.Logs, otlp.ContentTypeProtobuf, otlptest.Protobuf(otlp.Logs, nil, spanName(0), nil))
	h.send(otlp.Logs, otlp.ContentTypeProtobuf, otlptest.Protobuf(otlp.Logs, nil, spanName(1), nil))
	h.closeInput()
	r := h.report()
	if r.Outcome != protocol.OutcomeSucceeded || r.Telemetry == nil || r.Telemetry.Dropped != 2 || r.Telemetry.Shipped != 0 {
		t.Fatalf("report %+v %+v", r, r.Telemetry)
	}
	var exit, result time.Time
	for _, e := range h.fake.Events() {
		switch e.Kind {
		case "stdio-X":
			exit = e.At
		case "result":
			result = e.At
		}
	}
	if held := result.Sub(exit); held < 900*time.Millisecond || held > 5*time.Second {
		t.Errorf("the result was held %s after the agent's exit; the bound is 1s", held)
	}
}

// Without a telemetry flow, nothing listens for OTLP: the machine gets
// no forward, the agent no OTLP variable, and the report no counts.
func TestNoTelemetryFlowNoReceiver(t *testing.T) {
	h := newHarness(t, defaults())
	h.offer("ONS-7", 1, map[string]string{"AGENT_DELAY": "500ms"})
	h.waitFor("the agent's answer", 20*time.Second, func() bool { return h.fake.Count("stdio-O", "ONS-7", 1) > 0 })
	if n := len(h.d.Listeners()); n != 2 {
		t.Errorf("the daemon listens on %d sockets; want egress and mcp", n)
	}
	h.waitFor("the report", 20*time.Second, func() bool { return h.fake.Count("report", "ONS-7", 1) > 0 })
	m := h.machine("ONS-7", 1)
	for _, f := range m.Spec().Forwards {
		if f.Name == protocol.FlowTelemetry || f.GuestPort == daemon.TelemetryGuestPort {
			t.Errorf("forward %+v without a telemetry flow", f)
		}
	}
	for k := range agentEnv(t, m) {
		if strings.HasPrefix(k, "OTEL_") || k == daemon.TraceparentEnv {
			t.Errorf("the agent got %s without a telemetry flow", k)
		}
	}
	for _, e := range h.fake.Events() {
		if e.Kind == "report" {
			var raw map[string]json.RawMessage
			_ = json.Unmarshal(e.Body, &raw)
			if _, ok := raw["telemetry"]; ok {
				t.Errorf("the report counts telemetry without a flow: %s", e.Body)
			}
		}
	}
}
