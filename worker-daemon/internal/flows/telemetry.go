package flows

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
)

// Telemetry is the guest's OTLP/HTTP receiver: it takes an export
// request on /v1/traces, /v1/logs or /v1/metrics, in protobuf or JSON,
// with no credential, stamps its resources with Attrs, masks it with
// Mask, and spools it, answering at once. A shipper sends the spool, the
// oldest request first, on the telemetry flow with the job's telemetry
// token. The spool holds at most SpoolBytes; a request that does not
// fit drops the oldest first. A refusing flow is tried again, never
// holding up the guest: what it never takes is dropped and counted.
type Telemetry struct {
	Job   *Job
	Attrs []otlp.Attr
	// Mask masks every string of a request before it is spooled.
	Mask func(string) string
	// SpoolBytes bounds the spool; 0 is protocol.DefaultSpoolBytes.
	SpoolBytes int64
	Log        *slog.Logger
	// Gone is told when the flow answers 410: the control plane no
	// longer wants the job.
	Gone func(error)

	mu       sync.Mutex
	queue    []spooled
	size     int64
	inflight bool
	counts   protocol.TelemetryCounts
	refusing bool
	closed   bool
	changed  chan struct{}
	wake     chan struct{}
	kick     chan struct{}
	stop     context.CancelFunc
	done     chan struct{}
}

type spooled struct {
	signal, contentType string
	body                []byte
}

// telemetryTry bounds one request on the flow.
const telemetryTry = 10 * time.Second

// maxTelemetryBackoff is the longest wait between tries of a refusing
// flow.
const maxTelemetryBackoff = 2 * time.Second

func (t *Telemetry) bound() int64 {
	if t.SpoolBytes > 0 {
		return t.SpoolBytes
	}
	return protocol.DefaultSpoolBytes
}

func (t *Telemetry) log() *slog.Logger {
	if t.Log != nil {
		return t.Log
	}
	return slog.Default()
}

// Start starts the shipper; it runs until [Telemetry.Close].
func (t *Telemetry) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.changed = make(chan struct{})
	t.wake = make(chan struct{}, 1)
	t.kick = make(chan struct{}, 1)
	t.stop = cancel
	t.done = make(chan struct{})
	t.mu.Unlock()
	go t.ship(ctx)
}

// Counts is what became of the requests so far.
func (t *Telemetry) Counts() protocol.TelemetryCounts {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts
}

// Spooled is how many requests, and how many bytes, the spool holds.
func (t *Telemetry) Spooled() (int, int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.queue), t.size
}

// notify wakes whoever waits on a change; t.mu is held.
func (t *Telemetry) notify() {
	close(t.changed)
	t.changed = make(chan struct{})
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (t *Telemetry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sig, ok := strings.CutPrefix(r.URL.Path, "/v1/")
	if !ok || !isSignal(sig) {
		http.Error(w, "OTLP/HTTP takes /v1/traces, /v1/logs and /v1/metrics", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "OTLP/HTTP takes POST", http.StatusMethodNotAllowed)
		return
	}
	enc, err := otlp.Encoding(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnsupportedMediaType)
		return
	}
	body, status, err := t.readBody(r)
	if err != nil {
		t.dropReceived(int64(len(body)))
		http.Error(w, err.Error(), status)
		return
	}
	mask := t.Mask
	if mask == nil {
		mask = func(s string) string { return s }
	}
	out, err := otlp.Rewrite(sig, enc, body, t.Attrs, mask)
	if err != nil {
		t.dropReceived(int64(len(body)))
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	t.enqueue(spooled{signal: sig, contentType: enc, body: out})
	w.Header().Set("Content-Type", enc)
	w.WriteHeader(http.StatusOK)
	if enc == otlp.ContentTypeJSON {
		_, _ = w.Write([]byte("{}"))
	}
}

func isSignal(s string) bool { return slices.Contains(otlp.Signals, s) }

// readBody reads a request's body, gzip or not, up to the spool's bound.
func (t *Telemetry) readBody(r *http.Request) ([]byte, int, error) {
	limit := t.bound()
	var src io.Reader = r.Body
	switch ce := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); ce {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(io.LimitReader(r.Body, limit+1))
		if err != nil {
			return nil, http.StatusBadRequest, fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		src = zr
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Errorf("content encoding %q is neither gzip nor identity", ce)
	}
	b, err := io.ReadAll(io.LimitReader(src, limit+1))
	if err != nil {
		return nil, http.StatusBadRequest, err
	}
	if int64(len(b)) > limit {
		return b, http.StatusRequestEntityTooLarge, fmt.Errorf("the request exceeds the spool's %d bytes", limit)
	}
	return b, 0, nil
}

func (t *Telemetry) dropReceived(n int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts.Received++
	t.counts.Dropped++
	t.counts.DroppedBytes += n
}

// enqueue spools s, dropping the oldest requests until it fits.
func (t *Telemetry) enqueue(s spooled) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.counts.Received++
	if t.closed {
		t.counts.Dropped++
		t.counts.DroppedBytes += int64(len(s.body))
		return
	}
	t.push(s, false)
	signal(t.wake)
}

// push puts s in the spool, at the front when it goes back after a
// refusal, and drops the oldest until the spool is within its bound;
// t.mu is held.
func (t *Telemetry) push(s spooled, front bool) {
	if front {
		t.queue = append([]spooled{s}, t.queue...)
	} else {
		t.queue = append(t.queue, s)
	}
	t.size += int64(len(s.body))
	dropped := int64(0)
	for t.size > t.bound() && len(t.queue) > 0 {
		old := t.queue[0]
		t.queue = t.queue[1:]
		t.size -= int64(len(old.body))
		t.counts.Dropped++
		t.counts.DroppedBytes += int64(len(old.body))
		dropped++
	}
	if dropped > 0 {
		t.log().Warn("telemetry spool full: dropped the oldest requests", slog.Int64("dropped", dropped), slog.Int64("spool_bytes", t.bound()))
	}
	t.notify()
}

// pop takes the oldest request for the shipper.
func (t *Telemetry) pop() (spooled, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.queue) == 0 {
		return spooled{}, false
	}
	s := t.queue[0]
	t.queue = t.queue[1:]
	t.size -= int64(len(s.body))
	t.inflight = true
	return s, true
}

// outcome is what became of one try.
type outcome int

const (
	shipped outcome = iota
	refused         // the flow will never take it
	retry
	jobGone
)

func (t *Telemetry) settle(s spooled, o outcome, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.inflight = false
	switch o {
	case shipped:
		t.counts.Shipped++
		if t.refusing {
			t.refusing = false
			t.log().Info("telemetry flow answers again")
		}
	case retry:
		if !t.refusing {
			t.refusing = true
			t.log().Warn("telemetry flow refuses: keeping the spool and trying again", slog.String("error", err.Error()))
		}
		t.push(s, true)
		return
	default:
		t.counts.Dropped++
		t.counts.DroppedBytes += int64(len(s.body))
		t.log().Warn("telemetry request refused: dropped", slog.String("signal", s.signal), slog.String("error", err.Error()))
	}
	t.notify()
}

func (t *Telemetry) ship(ctx context.Context) {
	defer close(t.done)
	wait := 100 * time.Millisecond
	for {
		s, ok := t.pop()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-t.wake:
			case <-t.kick:
			}
			continue
		}
		o, err := t.send(ctx, s)
		t.settle(s, o, err)
		switch o {
		case jobGone:
			if t.Gone != nil {
				t.Gone(err)
			}
		case retry:
			select {
			case <-ctx.Done():
				return
			case <-t.kick:
			case <-time.After(wait):
			}
			wait = min(2*wait, maxTelemetryBackoff)
			continue
		}
		wait = 100 * time.Millisecond
	}
}

func (t *Telemetry) send(ctx context.Context, s spooled) (outcome, error) {
	ctx, cancel := context.WithTimeout(ctx, telemetryTry)
	defer cancel()
	path := protocol.TelemetryPath(t.Job.ID, t.Job.Attempt, s.signal)
	resp, err := t.Job.request(ctx, protocol.FlowTelemetry, http.MethodPost, path, bytes.NewReader(s.body), s.contentType)
	if err != nil {
		return retry, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return shipped, nil
	}
	err = statusError(protocol.FlowTelemetry, path, resp)
	switch resp.StatusCode {
	case http.StatusGone:
		return jobGone, err
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return refused, err
	}
	return retry, err
}

// Flush waits until the spool is shipped, or ctx ends, and reports
// whether it was shipped. A shipper waiting to try a refusing flow again
// tries it at once.
func (t *Telemetry) Flush(ctx context.Context) bool {
	signal(t.kick)
	for {
		t.mu.Lock()
		empty := len(t.queue) == 0 && !t.inflight
		ch := t.changed
		t.mu.Unlock()
		if empty {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-ch:
		}
	}
}

// Close stops the shipper, drops what is still spooled, and returns the
// counts. Requests that arrive later are dropped and counted.
func (t *Telemetry) Close() protocol.TelemetryCounts {
	t.mu.Lock()
	t.closed = true
	stop, done := t.stop, t.done
	t.mu.Unlock()
	if stop != nil {
		stop()
		<-done
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if n := len(t.queue); n > 0 {
		t.counts.Dropped += int64(n)
		t.counts.DroppedBytes += t.size
		t.log().Warn("telemetry not shipped before the attempt ended: dropped", slog.Int("requests", n), slog.Int64("bytes", t.size))
		t.queue, t.size = nil, 0
	}
	return t.counts
}

// HasTelemetry reports whether the job's offer named a telemetry flow.
func (j *Job) HasTelemetry() bool {
	_, ok := j.Flows[protocol.FlowTelemetry]
	return ok
}
