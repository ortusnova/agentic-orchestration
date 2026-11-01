package otlp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
)

// collector is a fake OTLP/HTTP endpoint that answers with the scripted
// statuses in turn, then 200, and records what it received.
type collector struct {
	mu       sync.Mutex
	statuses []int
	requests []received
	srv      *httptest.Server
}

type received struct {
	path, contentType, auth string
	spans, status           int
}

func newCollector(t *testing.T, statuses ...int) *collector {
	t.Helper()
	c := &collector{statuses: statuses}
	c.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req exportRequest
		_ = json.Unmarshal(body, &req)
		n := 0
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				n += len(ss.Spans)
			}
		}
		c.mu.Lock()
		status := http.StatusOK
		if len(c.statuses) > 0 {
			status, c.statuses = c.statuses[0], c.statuses[1:]
		}
		c.requests = append(c.requests, received{r.URL.Path, r.Header.Get("Content-Type"), r.Header.Get("Authorization"), n, status})
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(c.srv.Close)
	return c
}

func (c *collector) got() []received {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]received(nil), c.requests...)
}

func (c *collector) waitSpans(t *testing.T, want int) []received {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		total := 0
		for _, r := range c.got() {
			if r.path == "/v1/traces" && r.status/100 == 2 {
				total += r.spans
			}
		}
		if total >= want {
			return c.got()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("collector requests = %+v, want %d spans accepted", c.got(), want)
	return nil
}

func fastConfig(endpoint string) Config {
	return Config{
		Endpoint: endpoint, BatchInterval: 10 * time.Millisecond, MinBackoff: 10 * time.Millisecond,
		MaxBackoff: 50 * time.Millisecond, RequestTimeout: 200 * time.Millisecond,
		Logger: slog.New(slog.DiscardHandler),
	}
}

func spanN(i int) telemetry.Span {
	return telemetry.Span{Kind: telemetry.KindTurn, SpanID: [8]byte{byte(i)}, Origin: telemetry.OriginDriver}
}

func runExporter(t *testing.T, e *Exporter) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); e.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return cancel
}

func TestExporter_SendsBatchesWithHeaders(t *testing.T) {
	t.Parallel()

	c := newCollector(t)
	cfg := fastConfig(c.srv.URL + "/")
	cfg.Headers = http.Header{"Authorization": {"Bearer collector-token"}}
	e := New(cfg)
	runExporter(t, e)
	for i := range 3 {
		e.Enqueue(spanN(i))
	}
	for _, r := range c.waitSpans(t, 3) {
		if r.path != "/v1/traces" || r.contentType != "application/json" || r.auth != "Bearer collector-token" {
			t.Errorf("request = %+v, want POST /v1/traces with JSON and the configured header", r)
		}
	}
}

func TestExporter_TransientFailureLosesNothing(t *testing.T) {
	t.Parallel()

	c := newCollector(t, http.StatusServiceUnavailable)
	e := New(fastConfig(c.srv.URL))
	runExporter(t, e)
	for i := range 3 {
		e.Enqueue(spanN(i))
	}
	got := c.waitSpans(t, 3)
	if len(got) < 2 || got[0].status != http.StatusServiceUnavailable || got[1].spans != 3 || e.Dropped() != 0 {
		t.Errorf("requests = %+v, dropped %d; want the 503'd batch sent again whole", got, e.Dropped())
	}
}

func TestExporter_RefusedBatchIsDroppedAndTheNextSent(t *testing.T) {
	t.Parallel()

	c := newCollector(t, http.StatusBadRequest)
	e := New(fastConfig(c.srv.URL))
	runExporter(t, e)
	e.Enqueue(spanN(1))
	deadline := time.Now().Add(2 * time.Second)
	for e.Dropped() != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	e.Enqueue(spanN(2))
	got := c.waitSpans(t, 1)
	if e.Dropped() != 1 || len(got) != 2 {
		t.Errorf("requests = %+v, dropped %d; want the 400'd batch dropped once and the next accepted", got, e.Dropped())
	}
}

// logBuffer is a slog handler target that tests can read safely.
type logBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func TestExporter_HangingEndpointDropsOldestAndLogsTheCount(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })

	logs := &logBuffer{}
	cfg := fastConfig(srv.URL)
	cfg.BufferSize, cfg.BatchSize = 10, 5
	cfg.Logger = slog.New(slog.NewTextHandler(logs, nil))
	e := New(cfg)
	runExporter(t, e)

	start := time.Now()
	for i := range 50 {
		e.Enqueue(spanN(i))
	}
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("enqueueing 50 spans took %v while the endpoint hangs, want it to return at once", took)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !strings.Contains(logs.String(), "dropped_spans=") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.Dropped() < 35 || !strings.Contains(logs.String(), "dropped_spans=") {
		t.Errorf("dropped %d, log %q; want the overflow dropped and its count logged", e.Dropped(), logs.String())
	}
}

func TestExporter_FlushIsBoundedWhenTheEndpointIsDown(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); hanging.Close() })
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	for name, url := range map[string]string{"hanging": hanging.URL, "refusing connections": closed.URL} {
		cfg := fastConfig(url)
		cfg.RequestTimeout = time.Minute // only the flush bound may end the attempt
		e := New(cfg)
		e.Enqueue(spanN(1))
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		start := time.Now()
		e.Flush(ctx)
		cancel()
		if took := time.Since(start); took > time.Second {
			t.Errorf("%s: Flush took %v, want it bounded by its context", name, took)
		}
	}
}

func TestExporter_FlushSendsWhatRunLeft(t *testing.T) {
	t.Parallel()

	c := newCollector(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	cfg := fastConfig(c.srv.URL)
	cfg.MinBackoff, cfg.MaxBackoff = time.Hour, time.Hour // Run gives up for now after one try
	e := New(cfg)
	cancel := runExporter(t, e)
	e.Enqueue(spanN(1))
	e.Enqueue(spanN(2))
	deadline := time.Now().Add(2 * time.Second)
	for len(c.got()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	e.Enqueue(spanN(3))

	c.mu.Lock()
	c.statuses = nil
	c.mu.Unlock()
	ctx, stop := context.WithTimeout(context.Background(), FinalFlushBound)
	defer stop()
	e.Flush(ctx)
	if got := c.waitSpans(t, 3); e.Dropped() != 0 {
		t.Errorf("requests = %+v, dropped %d; want the retried batch and the late span flushed", got, e.Dropped())
	}
}

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	tests := map[string]time.Duration{
		"":                              0,
		"7":                             7 * time.Second,
		"-1":                            0,
		"soon":                          0,
		"Sun, 11 Oct 2026 12:00:30 GMT": 30 * time.Second,
		"Sun, 11 Oct 2026 11:00:00 GMT": 0,
	}
	for header, want := range tests {
		if got := retryAfter(header, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", header, got, want)
		}
	}
	e := New(Config{MinBackoff: time.Second, MaxBackoff: 30 * time.Second})
	if got := e.nextBackoff(time.Second, 10*time.Second); got != 10*time.Second {
		t.Errorf("nextBackoff with Retry-After 10s = %v, want 10s", got)
	}
	if got := e.nextBackoff(20*time.Second, 0); got != 30*time.Second {
		t.Errorf("nextBackoff past the cap = %v, want 30s", got)
	}
}
