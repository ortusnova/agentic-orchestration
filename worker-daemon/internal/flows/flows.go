// Package flows carries one job's data-plane traffic: the workspace
// seed and the result bundle on bundles, token usage on usage, the
// agent's standard input and output on stdio, and, through the two
// guest-facing forwarders, its model traffic on egress and its tool
// calls on mcp.
//
// Each request carries that flow's job token, which the daemon adds
// itself: the guest never sees one. Tokens stay in memory.
package flows

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/ws"
)

// Access is where one flow is and the token that admits a job to it.
type Access struct {
	URL   *url.URL
	Token secret.Token
}

// Job holds a job's flows.
type Job struct {
	ID      string
	Attempt int
	Flows   map[string]Access
	HTTP    *http.Client
	TLS     *tls.Config
}

// New checks an offer's flows: each one present, with an absolute
// http, https, ws or wss URL.
func New(o *protocol.Offer, client *http.Client, tlsConfig *tls.Config) (*Job, error) {
	j := &Job{ID: o.Job.ID, Attempt: o.Job.Attempt, Flows: map[string]Access{}, HTTP: client, TLS: tlsConfig}
	for _, name := range protocol.Flows {
		fa, ok := o.Flows[name]
		if !ok {
			return nil, fmt.Errorf("flows: the offer has no %s flow", name)
		}
		u, err := url.Parse(fa.URL)
		if err != nil || u.Host == "" || u.User != nil {
			return nil, fmt.Errorf("flows: %s: %q is not an absolute URL without user information", name, fa.URL)
		}
		switch u.Scheme {
		case "http", "https", "ws", "wss":
		default:
			return nil, fmt.Errorf("flows: %s: scheme %q is not http, https, ws or wss", name, u.Scheme)
		}
		j.Flows[name] = Access{URL: u, Token: fa.Token}
	}
	return j, nil
}

// Join returns base with path appended to its path.
func Join(base *url.URL, path string) *url.URL {
	u := *base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	return &u
}

func httpScheme(u *url.URL) *url.URL {
	v := *u
	switch v.Scheme {
	case "ws":
		v.Scheme = "http"
	case "wss":
		v.Scheme = "https"
	}
	return &v
}

func (j *Job) request(ctx context.Context, flow, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	a := j.Flows[flow]
	req, err := http.NewRequestWithContext(ctx, method, Join(httpScheme(a.URL), path).String(), body)
	if err != nil {
		return nil, err
	}
	if !a.Token.Empty() {
		req.Header.Set("Authorization", "Bearer "+a.Token.Reveal())
	}
	req.Header.Set(protocol.ProtocolHeader, strconv.Itoa(protocol.Version))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := j.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flows: %s %s: %w", flow, path, err)
	}
	return resp, nil
}

func statusError(flow, path string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("flows: %s %s: HTTP %d: %s", flow, path, resp.StatusCode, bytes.TrimSpace(b))
}

// FetchSeed writes the workspace seed at seedPath on the bundles flow
// to w.
func (j *Job) FetchSeed(ctx context.Context, seedPath string, w io.Writer) error {
	if !strings.HasPrefix(seedPath, "/") {
		return fmt.Errorf("flows: seed_path %q is not a path", seedPath)
	}
	resp, err := j.request(ctx, protocol.FlowBundles, http.MethodGet, seedPath, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return statusError(protocol.FlowBundles, seedPath, resp)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

// BundleContentType is the media type of a git bundle.
const BundleContentType = "application/x-git-bundle"

// UploadResult sends the bundle of the run's commits on the bundles
// flow.
func (j *Job) UploadResult(ctx context.Context, bundle io.Reader) error {
	path := protocol.ResultPath(j.ID, j.Attempt)
	resp, err := j.request(ctx, protocol.FlowBundles, http.MethodPut, path, bundle, BundleContentType)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return statusError(protocol.FlowBundles, path, resp)
	}
	return nil
}

// PostUsage sends the run's token usage on the usage flow.
func (j *Job) PostUsage(ctx context.Context, u protocol.Usage) error {
	b, err := json.Marshal(u)
	if err != nil {
		return err
	}
	path := protocol.AttemptSuffix(j.ID, j.Attempt)
	resp, err := j.request(ctx, protocol.FlowUsage, http.MethodPost, path, bytes.NewReader(b), "application/json")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return statusError(protocol.FlowUsage, path, resp)
	}
	return nil
}

// DialStdio opens the job's stdio stream, a WebSocket.
func (j *Job) DialStdio(ctx context.Context) (*ws.Conn, error) {
	a := j.Flows[protocol.FlowStdio]
	u := Join(a.URL, protocol.AttemptSuffix(j.ID, j.Attempt))
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	h := http.Header{}
	if !a.Token.Empty() {
		h.Set("Authorization", "Bearer "+a.Token.Reveal())
	}
	h.Set(protocol.ProtocolHeader, strconv.Itoa(protocol.Version))
	c, err := ws.Dial(ctx, u.String(), h, j.TLS)
	if err != nil {
		return nil, fmt.Errorf("flows: stdio: %w", err)
	}
	return c, nil
}

// StdioWriter sends each write as one binary message.
type StdioWriter struct{ C *ws.Conn }

func (w StdioWriter) Write(p []byte) (int, error) {
	if err := w.C.WriteMessage(ws.OpBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// PumpStdin copies each message from c to w until c closes, then
// closes w.
func PumpStdin(c *ws.Conn, w io.WriteCloser) error {
	defer func() { _ = w.Close() }()
	for {
		_, msg, err := c.ReadMessage()
		if errors.Is(err, ws.ErrClosed) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := w.Write(msg); err != nil {
			return err
		}
	}
}
