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
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/stdio"
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

// StatusError is a flow request the control plane answered with a
// status that is not a success.
type StatusError struct {
	Flow, Path string
	Status     int
	Body       string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("flows: %s %s: HTTP %d: %s", e.Flow, e.Path, e.Status, e.Body)
}

func statusError(flow, path string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &StatusError{Flow: flow, Path: path, Status: resp.StatusCode, Body: string(bytes.TrimSpace(b))}
}

// FetchSeed writes the workspace seed at seedPath on the bundles flow
// to w; an empty seedPath is the attempt's own,
// /jobs/{job}/attempts/{attempt}/seed.
func (j *Job) FetchSeed(ctx context.Context, seedPath string, w io.Writer) error {
	if seedPath == "" {
		seedPath = protocol.SeedPath(j.ID, j.Attempt)
	}
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

// Result is what the worker returns when the agent's session ended.
type Result struct {
	// Bundle is the bundle of the issue branch; nil when the machine
	// has no issue branch, and the part is then left out.
	Bundle io.Reader
	// PR is the agent's .sortie/pr.json; nil leaves the part out.
	PR []byte
}

// UploadResult returns the result on the bundles flow: POST
// /jobs/{job}/attempts/{attempt}/result, multipart/form-data with the
// part bundle and the optional part pr.
func (j *Job) UploadResult(ctx context.Context, res Result) error {
	path := protocol.ResultPath(j.ID, j.Attempt)
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		pw.CloseWithError(writeResult(mw, res))
	}()
	resp, err := j.request(ctx, protocol.FlowBundles, http.MethodPost, path, pr, mw.FormDataContentType())
	_ = pr.Close()
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return statusError(protocol.FlowBundles, path, resp)
	}
	return nil
}

func writeResult(mw *multipart.Writer, res Result) error {
	part := func(name, file, contentType string) (io.Writer, error) {
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, name, file))
		h.Set("Content-Type", contentType)
		return mw.CreatePart(h)
	}
	if res.Bundle != nil {
		w, err := part(protocol.ResultBundlePart, "result.bundle", BundleContentType)
		if err != nil {
			return err
		}
		if _, err := io.Copy(w, res.Bundle); err != nil {
			return err
		}
	}
	if res.PR != nil {
		w, err := part(protocol.ResultPRPart, "pr.json", "application/json")
		if err != nil {
			return err
		}
		if _, err := w.Write(res.PR); err != nil {
			return err
		}
	}
	return mw.Close()
}

// PostUsage sends the attempt's token totals on the usage flow.
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

// DialStdio opens the job's stdio stream: GET
// /jobs/{job}/attempts/{attempt} with Upgrade: sortie-stdio/1. A ws or
// wss flow URL is dialled as http or https.
func (j *Job) DialStdio(ctx context.Context) (*stdio.Conn, error) {
	a := j.Flows[protocol.FlowStdio]
	u := Join(httpScheme(a.URL), protocol.AttemptSuffix(j.ID, j.Attempt))
	h := http.Header{}
	if !a.Token.Empty() {
		h.Set("Authorization", "Bearer "+a.Token.Reveal())
	}
	h.Set(protocol.ProtocolHeader, strconv.Itoa(protocol.Version))
	c, err := stdio.Dial(ctx, u.String(), h, j.TLS)
	if err != nil {
		return nil, fmt.Errorf("flows: stdio: %w", err)
	}
	return c, nil
}
