package livetest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// offer is the part of a worker API offer the tests use.
type offer struct {
	Job struct {
		ID      string `json:"id"`
		Attempt int    `json:"attempt"`
		Issue   struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
		} `json:"issue"`
	} `json:"job"`
	Run struct {
		Credentials []struct {
			Name        string `json:"name"`
			Placeholder string `json:"placeholder"`
		} `json:"credentials"`
	} `json:"run"`
	Flows map[string]struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	} `json:"flows"`
}

func (o *offer) suffix() string { return fmt.Sprintf("/jobs/%s/attempts/%d", o.Job.ID, o.Job.Attempt) }

// job is one leased attempt that the test runs as the worker would: it
// opens the stdio flow and answers Sortie's session as the agent.
type job struct {
	l       live
	t       *testing.T
	o       offer
	raw     []byte         // the poll's answer, as the worker daemon reads it
	conn    io.WriteCloser // the frames to Sortie, through flowprobe
	probe   *exec.Cmd
	wmu     sync.Mutex
	prompt  chan json.RawMessage // the id of the session/prompt request
	release chan struct{}
	done    chan struct{}

	lastStatus int // of the last CONNECT on the egress flow
}

// takeJobs checks p1 in with room for n jobs and polls until it holds n.
func takeJobs(t *testing.T, l live, n int) []*job {
	t.Helper()
	decl := map[string]any{
		"os": "linux", "arch": "amd64", "kvm": true, "nested": true, "backends": []string{"smolvm"},
		"run_slots": n, "running": 0, "free": map[string]float64{"cpu": 8, "mem_gib": 32, "disk_gib": 100},
	}
	var jobs []*job
	eventually(t, fmt.Sprintf("%d offers for p1", n), 3*time.Minute, func() bool {
		if status, err := l.checkin(t, "p1", decl, ""); err != nil || status != http.StatusOK {
			t.Logf("check-in: %d %v", status, err)
			return false
		}
		status, body, err := l.workerCall(t, "p1", "/api/v1/workers/poll", nil, "")
		if err != nil || status != http.StatusOK {
			return false
		}
		var o offer
		if err := json.Unmarshal(body, &o); err != nil {
			t.Fatalf("offer: %v: %s", err, body)
		}
		jobs = append(jobs, &job{l: l, t: t, o: o, raw: body, prompt: make(chan json.RawMessage, 1), release: make(chan struct{}), done: make(chan struct{})})
		return len(jobs) == n
	})
	return jobs
}

// flowURL returns the URL of path on a flow of the job, and checks it is
// Pomerium's.
func (j *job) flowURL(flow, path string) string {
	f, ok := j.o.Flows[flow]
	if !ok || f.Token == "" {
		j.t.Fatalf("the offer of %s has no %s flow", j.o.Job.ID, flow)
	}
	if !strings.HasPrefix(f.URL, j.l.publicURL()) {
		j.t.Fatalf("flow %s is %s, not on Pomerium %s", flow, f.URL, j.l.publicURL())
	}
	return strings.TrimSuffix(f.URL, "/") + path
}

func (j *job) token(flow string) string { return j.o.Flows[flow].Token }

// openStdio opens the job's stdio stream through Pomerium with the
// worker daemon's own client (flowprobe stdio), which asks for
// sortie-stdio/2 as the daemon does, and answers the session as an ACP
// agent until released.
func (j *job) openStdio(ctx context.Context) {
	j.t.Helper()
	j.flowURL("stdio", j.o.suffix())
	cmd := j.flowprobe(ctx, "stdio")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		j.t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		j.t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		j.t.Fatal(err)
	}
	br := bufio.NewReader(stdout)
	proto, err := br.ReadString('\n')
	if err != nil {
		_ = cmd.Wait()
		j.t.Fatalf("stdio of %s through the worker daemon's client: %v: %s", j.o.Job.ID, err, stderr.String())
	}
	if proto = strings.TrimSpace(proto); proto != "sortie-stdio/2" {
		j.t.Errorf("stdio of %s agreed %s, want sortie-stdio/2, which the worker daemon asks for first", j.o.Job.ID, proto)
	}
	j.conn, j.probe = stdin, cmd
	go j.agent(br)
}

// flowprobe runs the worker daemon's flowprobe (run.sh builds it) with
// p1's certificate and this job's offer, in the control plane's image
// on the host's network, where cp.test is this host, so it dials
// Pomerium by the offer's URLs as a worker host does.
func (j *job) flowprobe(ctx context.Context, args ...string) *exec.Cmd {
	j.t.Helper()
	dir := os.Getenv("CP_LIVE_DIR")
	if dir == "" || os.Getenv("CP_TAG") == "" {
		j.t.Fatal("CP_LIVE_DIR and CP_TAG must name run.sh's directory and images")
	}
	offerFile := filepath.Join(dir, "offer-"+j.o.Job.ID+".json")
	if err := os.WriteFile(offerFile, j.raw, 0o600); err != nil {
		j.t.Fatal(err)
	}
	w := filepath.Join(j.l.home, "workers", "p1")
	argv := []string{"run", "--rm", "-i", "--network", "host", "--add-host", publicHost + ":127.0.0.1", "--user", "0",
		"-v", dir + ":" + dir + ":ro", "--entrypoint", filepath.Join(dir, "bin", "flowprobe"),
		"localhost/ortus-nova/control-plane:" + os.Getenv("CP_TAG"),
		"-offer", offerFile, "-cert", filepath.Join(w, "worker.crt"), "-key", filepath.Join(w, "worker.key"),
		"-ca", filepath.Join(j.l.home, "ca", "ca.crt")}
	return exec.CommandContext(ctx, "podman", append(argv, args...)...) // #nosec G204 -- the test's own arguments
}

func (j *job) frame(kind byte, payload []byte) {
	j.wmu.Lock()
	defer j.wmu.Unlock()
	head := make([]byte, 5)
	head[0] = kind
	binary.BigEndian.PutUint32(head[1:], uint32(len(payload))) // #nosec G115 -- the payloads here are small
	_, _ = j.conn.Write(append(head, payload...))
}

func (j *job) answer(id json.RawMessage, result any) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	j.frame('O', append(b, '\n'))
}

// agent reads Sortie's frames and answers as an ACP agent: the prompt is
// answered only once the test releases it.
func (j *job) agent(br *bufio.Reader) {
	defer close(j.done)
	var line []byte
	for {
		head := make([]byte, 5)
		if _, err := io.ReadFull(br, head); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint32(head[1:]))
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		switch head[0] {
		case 'S':
			// sortie-stdio/2 asks for the agent's .sortie files after a
			// turn; this agent wrote none.
			j.frame('F', []byte("{}"))
		case 'C':
			code := make([]byte, 4)
			j.frame('X', code)
			return
		case 'I':
			line = append(line, payload...)
		}
		for {
			i := bytes.IndexByte(line, '\n')
			if i < 0 {
				break
			}
			msg := line[:i]
			line = line[i+1:]
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if json.Unmarshal(msg, &m) != nil || m.Method == "" || len(m.ID) == 0 {
				continue
			}
			switch m.Method {
			case "initialize":
				j.answer(m.ID, map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"mcpCapabilities": map[string]any{"http": true}}, "authMethods": []any{},
					"agentInfo": map[string]any{"name": "live-test-agent", "version": "1"}})
			case "session/new":
				j.answer(m.ID, map[string]any{"sessionId": "live-" + j.o.Job.ID})
			case "session/prompt":
				j.prompt <- m.ID
				go func(id json.RawMessage) {
					<-j.release
					j.answer(id, map[string]any{"stopReason": "end_turn"})
				}(m.ID)
			default:
				b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
				j.frame('O', append(b, '\n'))
			}
		}
	}
}

// finish ends the turn, waits for Sortie to close the agent's input, and
// reports the attempt, so the lease ends.
func (j *job) finish() {
	close(j.release)
	select {
	case <-j.done:
	case <-time.After(2 * time.Minute):
		j.frame('X', make([]byte, 4))
	}
	_ = j.conn.Close()
	_ = j.probe.Wait()
	_, _, _ = j.l.workerCall(j.t, "p1", "/api/v1/workers"+j.o.suffix()+"/report",
		map[string]string{"outcome": "failed", "reason": "the live test ends the attempt", "machine": "deleted"}, "")
}

// mcpCall sends one JSON-RPC request to the tool server the mcp flow
// reaches for path with token, and returns the HTTP status and the
// JSON-RPC result.
func (j *job) mcpCall(path, token, method string, params any) (int, json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	req, err := http.NewRequest(http.MethodPost, j.flowURL("mcp", path), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(protocolHeader, "1")
	resp, err := j.l.client(j.t, "p1").Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, b, nil
	}
	// A streamable HTTP answer may come as one server-sent event.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		for l := range strings.SplitSeq(string(b), "\n") {
			if d, ok := strings.CutPrefix(l, "data: "); ok {
				b = []byte(d)
			}
		}
	}
	var rpc struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(b, &rpc); err != nil {
		return resp.StatusCode, nil, fmt.Errorf("tool server answer: %w: %s", err, b)
	}
	if rpc.Error != nil {
		return resp.StatusCode, nil, fmt.Errorf("tool server error: %s", rpc.Error)
	}
	return resp.StatusCode, rpc.Result, nil
}

// issueOfSession asks the session's tool server, through the mcp flow,
// which issue its history is of.
func (j *job) issueOfSession(path, token string) (string, error) {
	init := map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "live-test", "version": "1"}}
	if status, _, err := j.mcpCall(path, token, "initialize", init); err != nil || status != http.StatusOK {
		return "", fmt.Errorf("initialize: %d %v", status, err)
	}
	status, res, err := j.mcpCall(path, token, "tools/call", map[string]any{"name": "workspace_history", "arguments": map[string]any{}})
	if err != nil || status != http.StatusOK {
		return "", fmt.Errorf("workspace_history: %d %v", status, err)
	}
	var tool struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &tool); err != nil || len(tool.Content) == 0 {
		return "", fmt.Errorf("workspace_history answered %s", res)
	}
	var out struct {
		Data struct {
			IssueID string `json:"issue_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(tool.Content[0].Text), &out); err != nil {
		return "", fmt.Errorf("workspace_history answered %s", tool.Content[0].Text)
	}
	return out.Data.IssueID, nil
}

// TestJobs runs ONS-7 and ONS-8 at once on p1, through Pomerium, as the
// worker host would, and checks the scenarios that need live jobs.
func TestJobs(t *testing.T) {
	l := project(t)
	jobs := takeJobs(t, l, 2)
	byID := map[string]*job{}
	for _, j := range jobs {
		byID[j.o.Job.ID] = j
	}
	if byID["ONS-7"] == nil || byID["ONS-8"] == nil {
		t.Fatalf("p1 holds %v, want ONS-7 and ONS-8", []string{jobs[0].o.Job.ID, jobs[1].o.Job.ID})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, j := range jobs {
		j.openStdio(ctx)
		defer j.finish()
	}
	for _, j := range jobs {
		select {
		case <-j.prompt:
		case <-ctx.Done():
			t.Fatalf("Sortie did not prompt %s's session", j.o.Job.ID)
		}
	}

	// Scenario: Two jobs' tool calls. Jobs ONS-7 and ONS-8 call tools at
	// the same time; each call reaches only its own job's tool server.
	t.Run("Two jobs' tool calls", func(t *testing.T) {
		var wg sync.WaitGroup
		got := map[string]string{}
		var mu sync.Mutex
		for range 3 {
			for _, j := range jobs {
				wg.Go(func() {
					issue, err := j.issueOfSession(j.o.suffix()+"/mcp", j.token("mcp"))
					mu.Lock()
					defer mu.Unlock()
					if err != nil {
						t.Errorf("%s's tool call: %v", j.o.Job.ID, err)
						return
					}
					if prev, ok := got[j.o.Job.ID]; ok && prev != issue {
						t.Errorf("%s's tool calls reached the tool servers of %s and %s", j.o.Job.ID, prev, issue)
					}
					got[j.o.Job.ID] = issue
				})
			}
		}
		wg.Wait()
		for _, j := range jobs {
			if want := j.o.Job.Issue.ID; got[j.o.Job.ID] != want {
				t.Errorf("%s's tool call reached the tool server of issue %q, want its own %q", j.o.Job.ID, got[j.o.Job.ID], want)
			}
		}
	})

	t.Run("A token that is not the job's live mcp token is refused", func(t *testing.T) {
		a, b := byID["ONS-7"], byID["ONS-8"]
		for name, c := range map[string]struct{ path, token string }{
			"ONS-7's token on ONS-8's path":   {b.o.suffix() + "/mcp", a.token("mcp")},
			"ONS-7's usage token on its path": {a.o.suffix() + "/mcp", a.token("usage")},
			"no live token":                   {a.o.suffix() + "/mcp", "not-a-job-token"},
		} {
			status, _, _ := a.mcpCall(c.path, c.token, "tools/list", map[string]any{})
			if status != http.StatusUnauthorized {
				t.Errorf("%s: status %d, want 401 before any session", name, status)
			}
		}
	})

	egressCA := x509.NewCertPool()
	if !egressCA.AppendCertsFromPEM([]byte(secret(t, l, "cp-egress-ca-cert"))) {
		t.Fatal("no egress CA")
	}
	j := byID["ONS-7"]

	// Scenario: A model request. An agent in a guest calls the model API
	// with its placeholder; nono sends the request upstream with the real
	// credential, and logs it.
	t.Run("A model request", func(t *testing.T) {
		placeholder := ""
		for _, c := range j.o.Run.Credentials {
			if c.Name == "anthropic" {
				placeholder = c.Placeholder
			}
		}
		if placeholder == "" || strings.Contains(placeholder, secret(t, l, "cp-anthropic-token")) {
			t.Fatalf("the offer's model placeholder is %q; a guest must see only a placeholder", placeholder)
		}
		status, body := j.guestRequest(t, egressCA, http.MethodPost, "https://model.cp.test/v1/messages", "Bearer "+placeholder)
		if status != http.StatusOK {
			t.Fatalf("the model request: %d %s", status, body)
		}
		var seen struct {
			Authorization string `json:"authorization"`
		}
		_ = json.Unmarshal(body, &seen)
		if want := "Bearer " + secret(t, l, "cp-anthropic-token"); seen.Authorization != want {
			t.Fatalf("upstream saw Authorization %q, want the real credential", seen.Authorization)
		}
		eventually(t, "nono's log of the request", 30*time.Second, func() bool {
			return strings.Contains(logs(t, "cp-nono"), "model.cp.test")
		})
		if !strings.Contains(logs(t, "cp-egress-gate"), `msg="egress tunnel" worker=p1 job=ONS-7 attempt=`+fmt.Sprint(j.o.Job.Attempt)+` host=model.cp.test:443`) {
			t.Errorf("the egress gate did not log the tunnel with its worker and job:\n%s", logs(t, "cp-egress-gate"))
		}
	})

	t.Run("A path the policy does not allow", func(t *testing.T) {
		status, body := j.guestRequest(t, egressCA, http.MethodGet, "https://model.cp.test/admin", "Bearer x")
		if status == http.StatusOK && strings.Contains(string(body), `"path":"/admin"`) {
			t.Fatalf("nono let GET /admin through: %s", body)
		}
	})

	// The worker daemon's own egress forwarder, through Pomerium: the
	// gate admits its CONNECT to an allowed host, and nono refuses one it
	// does not list.
	t.Run("The worker daemon's egress forwarder", func(t *testing.T) {
		if status := j.daemonConnect(t, "model.cp.test:443"); status != http.StatusOK {
			t.Errorf("CONNECT model.cp.test:443: %d, want 200", status)
		}
		if status := j.daemonConnect(t, "pastebin.com:443"); status == http.StatusOK {
			t.Error("the forwarder reached pastebin.com")
		}
	})

	// Scenario: A disallowed host. An agent requests a host the policy
	// does not list; nono refuses it.
	t.Run("A disallowed host", func(t *testing.T) {
		status := j.connect(t, "pastebin.com:443")
		if status == http.StatusOK {
			t.Fatal("nono let the job reach pastebin.com")
		}
		eventually(t, "nono's log of the refusal", 30*time.Second, func() bool {
			return strings.Contains(logs(t, "cp-nono"), "pastebin.com")
		})
	})
}

// daemonConnect sends a CONNECT for host through the worker daemon's
// egress forwarder (flowprobe connect), as a guest does, and returns the
// status it got.
func (j *job) daemonConnect(t *testing.T, host string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := j.flowprobe(ctx, "connect", host)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("CONNECT %s through the worker daemon's forwarder: %v: %s", host, err, stderr.String())
	}
	var status int
	if _, err := fmt.Sscan(string(out), &status); err != nil {
		t.Fatalf("flowprobe connect printed %q", out)
	}
	return status
}

// connect sends a CONNECT for host on the job's egress flow and returns
// the status of the answer.
func (j *job) connect(t *testing.T, host string) int {
	t.Helper()
	conn, _ := j.tunnel(t, host)
	_ = conn.Close()
	return j.lastStatus
}

// guestRequest makes one HTTPS request as a guest would through the
// worker's egress forwarder: CONNECT on the egress flow with the job's
// egress token, then TLS to the host, trusting the egress CA.
func (j *job) guestRequest(t *testing.T, egressCA *x509.CertPool, method, rawURL, authorization string) (int, []byte) {
	t.Helper()
	u, _ := url.Parse(rawURL)
	conn, br := j.tunnel(t, u.Hostname()+":443")
	defer func() { _ = conn.Close() }()
	if j.lastStatus != http.StatusOK {
		return j.lastStatus, nil
	}
	inner := tls.Client(&bufConn{Conn: conn, r: br}, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname(), RootCAs: egressCA})
	req, _ := http.NewRequest(method, rawURL, strings.NewReader(`{"model":"live-test"}`))
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json")
	if err := req.Write(inner); err != nil {
		t.Fatalf("the request through the tunnel: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(inner), req)
	if err != nil {
		t.Fatalf("the answer through the tunnel: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b
}

// tunnel opens a CONNECT for host through Pomerium's egress route, as the
// worker daemon's egress forwarder does, and records its status.
func (j *job) tunnel(t *testing.T, host string) (net.Conn, *bufio.Reader) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := j.l.dial(ctx, j.l.tlsFor(t, "p1"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Bearer %s\r\n%s: 1\r\n\r\n", host, host, j.token("egress"), protocolHeader)
	if err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("CONNECT %s: %v", host, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		t.Logf("CONNECT %s: %s %s", host, resp.Status, b)
	}
	j.lastStatus = resp.StatusCode
	return conn, br
}

// bufConn reads what the CONNECT answer's reader already buffered first.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }
