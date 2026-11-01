package otlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
)

// Defaults for the exporter's limits.
const (
	DefaultBufferSize     = 2048
	DefaultBatchSize      = 256
	DefaultBatchInterval  = 2 * time.Second
	DefaultRequestTimeout = 10 * time.Second
	DefaultMinBackoff     = time.Second
	DefaultMaxBackoff     = 30 * time.Second

	// FinalFlushBound bounds the last export attempt as the broker exits.
	FinalFlushBound = 3 * time.Second

	// logEvery bounds how often drops and failures are logged.
	logEvery = time.Minute
)

// Config configures an Exporter.
type Config struct {
	// Endpoint is the OTLP/HTTP base URL; spans go to its /v1/traces
	// path.
	Endpoint string

	// Headers are sent with every request, for example credentials for
	// the Collector.
	Headers http.Header

	Encoder Encoder

	// HTTP sends the requests; nil selects a default client.
	HTTP *http.Client

	// Limits; zero selects the defaults.
	BufferSize     int
	BatchSize      int
	BatchInterval  time.Duration
	RequestTimeout time.Duration
	MinBackoff     time.Duration
	MaxBackoff     time.Duration

	Logger *slog.Logger
}

// Exporter sends ended spans to an OTLP/HTTP endpoint in batches, from its
// own goroutine. Enqueue never blocks: when the buffer is full, the oldest
// span is dropped and counted.
type Exporter struct {
	cfg Config
	url string
	log *slog.Logger

	mu       sync.Mutex
	buf      []telemetry.Span
	inflight []telemetry.Span // the batch being sent or retried
	dropped  int64

	// lastLogged and loggedDrops limit how often problems are logged.
	lastLogged  time.Time
	loggedDrops int64

	wake chan struct{}
}

// New returns an exporter for cfg. Start it with Run.
func New(cfg Config) *Exporter {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{}
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = DefaultBufferSize
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.BatchInterval <= 0 {
		cfg.BatchInterval = DefaultBatchInterval
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = DefaultMinBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Exporter{
		cfg:  cfg,
		url:  strings.TrimRight(cfg.Endpoint, "/") + "/v1/traces",
		log:  log,
		wake: make(chan struct{}, 1),
	}
}

// Enqueue adds an ended span to the buffer. It returns at once.
func (e *Exporter) Enqueue(sp telemetry.Span) {
	e.mu.Lock()
	e.buf = append(e.buf, sp)
	if over := len(e.buf) - e.cfg.BufferSize; over > 0 {
		e.buf = append(e.buf[:0:0], e.buf[over:]...)
		e.dropped += int64(over)
	}
	full := len(e.buf) >= e.cfg.BatchSize
	e.mu.Unlock()
	if full {
		select {
		case e.wake <- struct{}{}:
		default:
		}
	}
}

// Dropped returns how many spans were dropped because the buffer was full
// or the endpoint refused them.
func (e *Exporter) Dropped() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.dropped
}

// Run sends batches until ctx ends: when a batch is full, or every batch
// interval. A failed batch is retried with capped backoff and kept for
// Flush when ctx ends first.
func (e *Exporter) Run(ctx context.Context) {
	tick := time.NewTicker(e.cfg.BatchInterval)
	defer tick.Stop()
	backoff := time.Duration(0)
	for {
		if backoff > 0 {
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		} else {
			select {
			case <-ctx.Done():
				return
			case <-e.wake:
			case <-tick.C:
			}
		}
		batch := e.next()
		if len(batch) == 0 {
			backoff = 0
			continue
		}
		delay, err := e.send(ctx, batch)
		switch {
		case err == nil:
			e.settle(true)
			backoff = 0
		case errors.Is(err, errPermanent):
			e.settle(false)
			e.mu.Lock()
			e.dropped += int64(len(batch))
			e.mu.Unlock()
			e.report(err)
			backoff = 0
		default:
			e.report(err)
			backoff = e.nextBackoff(backoff, delay)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// Flush makes one attempt, bounded by ctx, to send every span still held,
// including a batch Run was retrying.
func (e *Exporter) Flush(ctx context.Context) {
	for ctx.Err() == nil {
		batch := e.next()
		if len(batch) == 0 {
			return
		}
		if _, err := e.send(ctx, batch); err != nil {
			e.mu.Lock()
			e.dropped += int64(len(batch) + len(e.buf))
			e.inflight, e.buf = nil, nil
			e.mu.Unlock()
			e.log.Warn("telemetry not exported at exit", slog.Any("error", err), slog.Int64("dropped_spans", e.Dropped()))
			return
		}
		e.settle(true)
	}
}

// next returns the batch to send: the one in flight, or a new one taken
// from the front of the buffer.
func (e *Exporter) next() []telemetry.Span {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inflight == nil {
		n := min(len(e.buf), e.cfg.BatchSize)
		if n == 0 {
			return nil
		}
		e.inflight = append([]telemetry.Span(nil), e.buf[:n]...)
		e.buf = append(e.buf[:0:0], e.buf[n:]...)
	}
	return e.inflight
}

// settle forgets the batch in flight once it was sent or refused.
func (e *Exporter) settle(sent bool) {
	e.mu.Lock()
	e.inflight = nil
	e.mu.Unlock()
	if sent {
		e.report(nil)
	}
}

// errPermanent marks a refusal that retrying cannot fix.
var errPermanent = errors.New("refused")

// send posts batch once. It returns the delay the endpoint asked for, if
// any, and an error wrapping errPermanent for a refusal not worth retrying.
func (e *Exporter) send(ctx context.Context, batch []telemetry.Span) (time.Duration, error) {
	body, err := e.cfg.Encoder.Encode(batch)
	if err != nil {
		return 0, fmt.Errorf("encode spans: %w: %w", errPermanent, err)
	}
	ctx, cancel := context.WithTimeout(ctx, e.cfg.RequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w: %w", errPermanent, err)
	}
	for name, values := range e.cfg.Headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.cfg.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close() //nolint:errcheck // the body is drained below; a close error changes nothing
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode/100 == 2:
		return 0, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode/100 == 5:
		return retryAfter(resp.Header.Get("Retry-After"), time.Now()), fmt.Errorf("endpoint answered %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	default:
		return 0, fmt.Errorf("endpoint answered %d: %w: %s", resp.StatusCode, errPermanent, strings.TrimSpace(string(msg)))
	}
}

func (e *Exporter) nextBackoff(current, asked time.Duration) time.Duration {
	next := e.cfg.MinBackoff
	if current > 0 {
		next = 2 * current
	}
	next = max(next, asked)
	return min(next, e.cfg.MaxBackoff)
}

// report logs a failure, or new drops, at most once per logEvery.
func (e *Exporter) report(err error) {
	e.mu.Lock()
	dropped := e.dropped
	newDrops := dropped > e.loggedDrops
	due := e.lastLogged.IsZero() || time.Since(e.lastLogged) >= logEvery
	if (err == nil && !newDrops) || !due {
		e.mu.Unlock()
		return
	}
	e.lastLogged = time.Now()
	e.loggedDrops = dropped
	e.mu.Unlock()
	if err != nil {
		e.log.Warn("telemetry export failed", slog.Any("error", err), slog.Int64("dropped_spans", dropped))
		return
	}
	e.log.Warn("telemetry spans dropped", slog.Int64("dropped_spans", dropped))
}

// retryAfter reads a Retry-After header: delay seconds or an HTTP date.
func retryAfter(header string, now time.Time) time.Duration {
	header = strings.TrimSpace(header)
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil && at.After(now) {
		return at.Sub(now)
	}
	return 0
}
