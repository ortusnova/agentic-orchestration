package flows

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp/otlptest"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
)

// telemetryFlow stands in for the telemetry flow: it records each
// request's path, headers and body, and answers status.
type telemetryFlow struct {
	*httptest.Server
	mu     sync.Mutex
	status int
	got    []*http.Request
	bodies [][]byte
}

func newTelemetryFlow(t *testing.T) *telemetryFlow {
	f := &telemetryFlow{status: http.StatusOK}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		status := f.status
		if status == http.StatusOK {
			f.got = append(f.got, r)
			f.bodies = append(f.bodies, b)
		}
		f.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *telemetryFlow) received() ([]*http.Request, [][]byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.got...), append([][]byte(nil), f.bodies...)
}

func newTestTelemetry(t *testing.T, flow *telemetryFlow, spool int64) *Telemetry {
	u, _ := url.Parse(flow.URL + "/flows/telemetry")
	j := &Job{ID: "ONS-7", Attempt: 2, HTTP: flow.Client(), Flows: map[string]Access{
		protocol.FlowTelemetry: {URL: u, Token: secret.New("telemetry-token-0123456789")},
	}}
	tel := &Telemetry{Job: j, Attrs: []otlp.Attr{{Key: "sortie.attempt", Value: "2"}}, SpoolBytes: spool}
	tel.Start()
	t.Cleanup(func() { tel.Close() })
	return tel
}

func post(t *testing.T, h http.Handler, method, path, contentType, encoding string, body []byte) int {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("Content-Type", contentType)
	if encoding != "" {
		r.Header.Set("Content-Encoding", encoding)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code
}

func TestTelemetryShipsWithTheFlowsTokenAndPath(t *testing.T) {
	flow := newTelemetryFlow(t)
	tel := newTestTelemetry(t, flow, 0)
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(otlptest.Protobuf(otlp.Metrics, nil, "m", nil))
	_ = zw.Close()
	if code := post(t, tel, http.MethodPost, "/v1/metrics", otlp.ContentTypeProtobuf, "gzip", gz.Bytes()); code != http.StatusOK {
		t.Fatalf("gzip request: %d", code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !tel.Flush(ctx) {
		t.Fatal("not flushed")
	}
	reqs, bodies := flow.received()
	if len(reqs) != 1 {
		t.Fatalf("shipped %d", len(reqs))
	}
	r := reqs[0]
	if r.URL.Path != "/flows/telemetry/jobs/ONS-7/attempts/2/v1/metrics" || r.Header.Get("Authorization") != "Bearer telemetry-token-0123456789" ||
		r.Header.Get(protocol.ProtocolHeader) != "1" || r.Header.Get("Content-Type") != otlp.ContentTypeProtobuf {
		t.Errorf("shipped %s %v", r.URL.Path, r.Header)
	}
	rs, err := otlp.Inspect(otlp.Metrics, otlp.ContentTypeProtobuf, bodies[0])
	if err != nil || rs[0].Attributes["sortie.attempt"] != "2" {
		t.Errorf("shipped %+v, %v", rs, err)
	}
	if c := tel.Close(); c != (protocol.TelemetryCounts{Received: 1, Shipped: 1}) {
		t.Errorf("counts %+v", c)
	}
}

func TestTelemetryReceiverRefusals(t *testing.T) {
	flow := newTelemetryFlow(t)
	tel := newTestTelemetry(t, flow, 0)
	body := otlptest.JSON(otlp.Traces, nil, "s", nil)
	for _, c := range []struct {
		method, path, ct, enc string
		body                  []byte
		want                  int
	}{
		{http.MethodPost, "/v1/profiles", otlp.ContentTypeJSON, "", body, http.StatusNotFound},
		{http.MethodGet, "/v1/traces", otlp.ContentTypeJSON, "", nil, http.StatusMethodNotAllowed},
		{http.MethodPost, "/v1/traces", "text/plain", "", body, http.StatusUnsupportedMediaType},
		{http.MethodPost, "/v1/traces", otlp.ContentTypeJSON, "br", body, http.StatusUnsupportedMediaType},
		{http.MethodPost, "/v1/traces", otlp.ContentTypeJSON, "", []byte("{"), http.StatusBadRequest},
		{http.MethodPost, "/v1/traces", otlp.ContentTypeProtobuf, "", body, http.StatusBadRequest},
	} {
		if got := post(t, tel, c.method, c.path, c.ct, c.enc, c.body); got != c.want {
			t.Errorf("%s %s %s %s: %d, want %d", c.method, c.path, c.ct, c.enc, got, c.want)
		}
	}
	if reqs, _ := flow.received(); len(reqs) != 0 {
		t.Errorf("shipped %d refused requests", len(reqs))
	}
}

func TestTelemetryARequestTheFlowRefusesForGoodIsDropped(t *testing.T) {
	flow := newTelemetryFlow(t)
	flow.status = http.StatusBadRequest
	tel := newTestTelemetry(t, flow, 0)
	if code := post(t, tel, http.MethodPost, "/v1/logs", otlp.ContentTypeJSON, "", otlptest.JSON(otlp.Logs, nil, "l", nil)); code != http.StatusOK {
		t.Fatal(code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !tel.Flush(ctx) {
		t.Fatal("not flushed")
	}
	if c := tel.Close(); c.Dropped != 1 || c.Shipped != 0 || c.DroppedBytes <= 0 {
		t.Errorf("counts %+v", c)
	}
}
