// Package cp is the daemon's client of the control plane's worker API,
// protocol 1. The daemon only dials: the control plane never reaches
// it.
package cp

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/config"
	"github.com/briancappello/agentic-orchestration/worker-daemon/internal/protocol"
)

// Errors the worker API answers with 409.
var (
	// ErrNotCheckedIn is a poll before the control plane knows the
	// worker.
	ErrNotCheckedIn = errors.New("cp: not checked in")
	// ErrLeaseLost is a heartbeat or report for an attempt the worker
	// no longer holds.
	ErrLeaseLost = errors.New("cp: lease lost")
)

// StatusError is any other refusal.
type StatusError struct {
	Route   string
	Status  int
	Code    string
	Message string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("cp: %s: HTTP %d %s: %s", e.Route, e.Status, e.Code, e.Message)
}

// TLSConfig returns the TLS settings of the control plane and its
// flows: the client certificate, once the control plane issues one,
// and the CA that signs the control plane. It is nil when neither is
// configured.
func TLSConfig(t config.TLS) (*tls.Config, error) {
	if t.CertFile == "" && t.CAFile == "" && t.ServerName == "" {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.ServerName}
	if t.CertFile != "" {
		cert, err := tls.LoadX509KeyPair(t.CertFile, t.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("cp: client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	if t.CAFile != "" {
		pem, err := os.ReadFile(t.CAFile)
		if err != nil {
			return nil, fmt.Errorf("cp: CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("cp: CA %s holds no PEM certificate", t.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// HTTPClient returns a client that dials directly, never through a
// proxy from the environment, with tlsConfig.
func HTTPClient(tlsConfig *tls.Config) *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     tlsConfig,
		ForceAttemptHTTP2:   tlsConfig != nil,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}}
}

// Client calls the worker API.
type Client struct {
	BaseURL        string
	WorkerID       string
	IdentityHeader string
	HTTP           *http.Client
	// Timeout bounds each request.
	Timeout time.Duration
}

func (c *Client) do(ctx context.Context, route, path string, body any) (int, []byte, error) {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+path, r)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set(protocol.ProtocolHeader, strconv.Itoa(protocol.Version))
	req.Header.Set(c.IdentityHeader, c.WorkerID)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("cp: %s: %w", route, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("cp: %s: %w", route, err)
	}
	return resp.StatusCode, b, nil
}

func refusal(route string, status int, b []byte) error {
	var eb protocol.ErrorBody
	_ = json.Unmarshal(b, &eb)
	return &StatusError{Route: route, Status: status, Code: eb.Error.Code, Message: eb.Error.Message}
}

// CheckIn declares the worker and returns the control plane's timings.
func (c *Client) CheckIn(ctx context.Context, d protocol.Declaration) (protocol.Timings, error) {
	status, b, err := c.do(ctx, "check-in", protocol.CheckInPath, d)
	if err != nil {
		return protocol.Timings{}, err
	}
	if status != http.StatusOK {
		return protocol.Timings{}, refusal("check-in", status, b)
	}
	var t protocol.Timings
	if err := json.Unmarshal(b, &t); err != nil || t.HeartbeatMS <= 0 {
		return protocol.Timings{}, fmt.Errorf("cp: check-in: answer holds no heartbeat_ms: %q", b)
	}
	return t, nil
}

// Poll asks for a job: nil when none fits.
func (c *Client) Poll(ctx context.Context) (*protocol.Offer, error) {
	status, b, err := c.do(ctx, "poll", protocol.PollPath, nil)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusConflict:
		return nil, ErrNotCheckedIn
	case http.StatusOK:
		var o protocol.Offer
		if err := json.Unmarshal(b, &o); err != nil {
			return nil, fmt.Errorf("cp: poll: unreadable offer: %w", err)
		}
		return &o, nil
	}
	return nil, refusal("poll", status, b)
}

// Heartbeat renews the lease on an attempt.
func (c *Client) Heartbeat(ctx context.Context, job string, attempt int) (protocol.Lease, error) {
	status, b, err := c.do(ctx, "heartbeat", protocol.AttemptPath(job, attempt, "heartbeat"), nil)
	if err != nil {
		return protocol.Lease{}, err
	}
	switch status {
	case http.StatusConflict:
		return protocol.Lease{}, ErrLeaseLost
	case http.StatusOK:
		var l protocol.Lease
		_ = json.Unmarshal(b, &l)
		return l, nil
	}
	return protocol.Lease{}, refusal("heartbeat", status, b)
}

// Report ends an attempt with its outcome.
func (c *Client) Report(ctx context.Context, job string, attempt int, r protocol.Report) error {
	status, b, err := c.do(ctx, "report", protocol.AttemptPath(job, attempt, "report"), r)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusNoContent, http.StatusOK:
		return nil
	case http.StatusConflict:
		return ErrLeaseLost
	}
	return refusal("report", status, b)
}
