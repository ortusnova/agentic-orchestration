package transport_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/transport"
)

const viewerToken = "viewer-token-for-tests-123"

func httpServer(t *testing.T, b *broker.Broker, cfg transport.HTTPConfig) *httptest.Server {
	t.Helper()
	return httpServerOn(t, b, cfg, nil)
}

// httpServerOn serves b, wrapping the listener with wrap when it is set.
func httpServerOn(t *testing.T, b *broker.Broker, cfg transport.HTTPConfig, wrap func(net.Listener) net.Listener) *httptest.Server {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = viewerToken
	}
	cfg.Logger = slog.New(slog.DiscardHandler)
	h, err := transport.NewHTTP(b, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(h.Handler())
	if wrap != nil {
		ts.Listener = wrap(ts.Listener)
	}
	ts.Start()
	t.Cleanup(ts.Close)
	return ts
}

// smallSendBuffers shrinks the send buffer of every accepted connection,
// so a client that stops reading stalls the server's writes quickly.
type smallSendBuffers struct{ net.Listener }

func (l smallSendBuffers) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(8192)
	}
	return conn, err
}

func request(t *testing.T, method, url string, header http.Header, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(req.Header, header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func auth(token string) http.Header { return http.Header{"Authorization": {"Bearer " + token}} }

// sseStream reads Server-Sent Events frames.
type sseStream struct {
	t    *testing.T
	resp *http.Response
	r    *bufio.Reader
}

type sseFrame struct {
	id       string
	data     string
	comments []string
}

func openEvents(t *testing.T, ts *httptest.Server, header http.Header) *sseStream {
	t.Helper()
	resp := request(t, http.MethodGet, ts.URL+"/events", header, "")
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		t.Fatalf("GET /events status = %d", resp.StatusCode)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &sseStream{t: t, resp: resp, r: bufio.NewReader(resp.Body)}
}

// next returns the next frame that carries data, keeping the comments
// seen before it.
func (s *sseStream) next() sseFrame {
	s.t.Helper()
	var f sseFrame
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			s.t.Fatalf("event stream ended: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if f.data != "" {
				return f
			}
		case strings.HasPrefix(line, ":"):
			f.comments = append(f.comments, strings.TrimSpace(line[1:]))
		case strings.HasPrefix(line, "id: "):
			f.id = line[4:]
		case strings.HasPrefix(line, "data: "):
			f.data = line[6:]
		}
	}
}

func (f sseFrame) event(t *testing.T) broker.Event {
	t.Helper()
	var ev broker.Event
	if err := json.Unmarshal([]byte(f.data), &ev); err != nil {
		t.Fatalf("decode %q: %v", f.data, err)
	}
	if f.id != "" && f.id != strconv.FormatInt(ev.Seq, 10) {
		t.Fatalf("frame id %s carries event seq %d", f.id, ev.Seq)
	}
	return ev
}

// produce makes the agent send n updates during the open driver turn and
// waits until the driver received them.
func produce(t *testing.T, s *session, n, size int) {
	t.Helper()
	pad := strings.Repeat("x", size)
	for i := range n {
		s.agent.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "s1", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": fmt.Sprintf("%d %s", i, pad)}},
		}})
		s.driver.nextWhere(func(m map[string]any) bool { return m["method"] == "session/update" })
	}
}

func TestNewHTTP_RefusesAListenerWithoutAToken(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, false)
	if _, err := transport.NewHTTP(s.b, transport.HTTPConfig{Token: "  "}); !errors.Is(err, transport.ErrNoViewerToken) {
		t.Errorf("NewHTTP without a token error = %v, want ErrNoViewerToken", err)
	}
}

func TestHTTP_Authentication(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{})
	tests := []struct {
		name   string
		method string
		path   string
		header http.Header
		want   int
	}{
		{"events without a token", http.MethodGet, "/events", nil, http.StatusUnauthorized},
		{"events with the token in the URL", http.MethodGet, "/events?access_token=" + viewerToken, nil, http.StatusUnauthorized},
		{"events with the token as a query parameter named token", http.MethodGet, "/events?token=" + viewerToken, nil, http.StatusUnauthorized},
		{"events with a wrong token", http.MethodGet, "/events", auth("wrong"), http.StatusUnauthorized},
		{"state without a token", http.MethodGet, "/state", nil, http.StatusUnauthorized},
		{"state with the token", http.MethodGet, "/state", auth(viewerToken), http.StatusOK},
		{"commands without a token", http.MethodPost, "/commands", nil, http.StatusUnauthorized},
	}
	for _, tt := range tests {
		resp := request(t, tt.method, ts.URL+tt.path, tt.header, `{"op":"state"}`)
		_ = resp.Body.Close()
		if resp.StatusCode != tt.want {
			t.Errorf("%s: status = %d, want %d", tt.name, resp.StatusCode, tt.want)
		}
	}
}

func TestHTTP_AnyoneCanFindTheSource(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	for _, tt := range []struct{ name, configured, want string }{
		{"default", "", transport.DefaultSourceURL},
		{"a modified broker's own", "https://git.example.com/our/broker", "https://git.example.com/our/broker"},
	} {
		ts := httpServer(t, s.b, transport.HTTPConfig{SourceURL: tt.configured})
		resp := request(t, http.MethodGet, ts.URL+"/source", nil, "")
		var got transport.About
		err := json.NewDecoder(resp.Body).Decode(&got)
		_ = resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: GET /source without a token = %d, %v", tt.name, resp.StatusCode, err)
		}
		if want := (transport.About{Name: "acp-broker", License: "AGPL-3.0-or-later", SourceURL: tt.want}); got != want {
			t.Errorf("%s: GET /source = %+v, want %+v", tt.name, got, want)
		}
	}
}

func TestHTTP_CommandsAnswerWithTheirResult(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{})

	resp := request(t, http.MethodPost, ts.URL+"/commands", auth(viewerToken), `{"op":"handback"}`)
	var ev broker.Event
	_ = json.NewDecoder(resp.Body).Decode(&ev)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || ev.Type != "error" || ev.Message != "not interrupted" {
		t.Errorf("handback without an interrupt = %d %+v, want 409 and the reason", resp.StatusCode, ev)
	}

	resp = request(t, http.MethodPost, ts.URL+"/commands", auth(viewerToken), `{"op":"interrupt"}`)
	_ = json.NewDecoder(resp.Body).Decode(&ev)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || ev.Type != "state" || ev.Floor != broker.FloorHuman {
		t.Errorf("interrupt = %d %+v, want 200 and the state with humans on the floor", resp.StatusCode, ev)
	}

	resp = request(t, http.MethodPost, ts.URL+"/commands", auth(viewerToken), `{"op":`)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed command status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTP_ResumesFromLastEventID(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{})
	produce(t, s, 300, 10)

	h := auth(viewerToken)
	h.Set("Last-Event-ID", "280")
	stream := openEvents(t, ts, h)
	if ev := stream.next().event(t); ev.Seq != 281 {
		t.Fatalf("first event after Last-Event-ID 280 = %+v, want seq 281", ev)
	}
}

func TestHTTP_ReportsEvictedEventsBeforeResuming(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{ReplayEvents: 10}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{})
	produce(t, s, 30, 10)

	h := auth(viewerToken)
	h.Set("Last-Event-ID", "5")
	stream := openEvents(t, ts, h)
	first := stream.next()
	if ev := first.event(t); first.id != "" || ev.Type != "error" || !strings.Contains(ev.Message, "6 to") {
		t.Fatalf("first frame = %+v, want an unnumbered note that events from 6 were evicted", first)
	}
	if next := stream.next(); next.id == "" {
		t.Fatalf("frame after the note has no id: %+v", next)
	}
}

func TestHTTP_SameEventsAsTheSocket(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{})
	sock := dialViewer(t, s.b)
	stream := openEvents(t, ts, auth(viewerToken))
	if ev := stream.next().event(t); ev.Type != "state" {
		t.Fatalf("first HTTP event = %+v, want the state snapshot", ev)
	}
	// Skip what each already had before the updates below.
	for range 1 {
		stream.next()
		sock.nextWhere(func(m map[string]any) bool { return m["seq"] != float64(0) })
	}
	produce(t, s, 5, 5)
	for range 5 {
		fromHTTP := stream.next().event(t)
		fromSocket := sock.nextWhere(func(map[string]any) bool { return true })
		if fromSocket["seq"] != float64(fromHTTP.Seq) || fromSocket["type"] != fromHTTP.Type {
			t.Fatalf("socket got %v, HTTP got %+v, want the same event", fromSocket, fromHTTP)
		}
	}
}

func TestHTTP_SendsHeartbeats(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{Heartbeat: 30 * time.Millisecond})
	stream := openEvents(t, ts, auth(viewerToken))
	stream.next() // the state snapshot
	stream.next() // the buffered state event of the open turn
	time.Sleep(100 * time.Millisecond)
	produce(t, s, 1, 1)
	if f := stream.next(); !slices.Contains(f.comments, "heartbeat") {
		t.Errorf("comments before the next event = %q, want a heartbeat", f.comments)
	}
}

func TestHTTP_SlowClientIsDisconnectedWithoutSlowingTheRelay(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServerOn(t, s.b, transport.HTTPConfig{WriteTimeout: 200 * time.Millisecond},
		func(ln net.Listener) net.Listener { return smallSendBuffers{ln} })

	// A client that sends its request and then never reads.
	conn, err := (&net.Dialer{}).DialContext(context.Background(), "tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "GET /events HTTP/1.1\r\nHost: x\r\nAuthorization: Bearer %s\r\n\r\n", viewerToken); err != nil {
		t.Fatal(err)
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetReadBuffer(4096)
	}

	start := time.Now()
	produce(t, s, 3000, 2048)
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("relaying 3000 updates took %v with a stalled viewer", elapsed)
	}

	// The server gave up on the client: draining what was sent reaches the
	// end of the connection.
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.Copy(io.Discard, conn); err != nil && !errors.Is(err, io.EOF) {
		t.Errorf("stalled client's connection did not end: %v", err)
	}
}

func TestHTTP_LoginSetsASessionCookie(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{PublicURL: "https://viewer.example"})

	resp := request(t, http.MethodPost, ts.URL+"/login", auth(viewerToken), "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login status = %d, want 204", resp.StatusCode)
	}
	cookies := resp.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login set %d cookies, want 1", len(cookies))
	}
	c := cookies[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || !c.Secure || c.MaxAge != int((12*time.Hour).Seconds()) {
		t.Errorf("cookie = %+v, want HttpOnly, SameSite=Strict, Path=/, Secure, and 12 hours", c)
	}

	// An EventSource sends no header, only the cookie.
	stream := openEvents(t, ts, http.Header{"Cookie": {c.Name + "=" + c.Value}})
	if ev := stream.next().event(t); ev.Type != "state" {
		t.Errorf("first event over the cookie = %+v", ev)
	}

	// Logout clears the cookie and ends the session.
	resp = request(t, http.MethodPost, ts.URL+"/logout", http.Header{"Cookie": {c.Name + "=" + c.Value}}, "")
	_ = resp.Body.Close()
	if cleared := resp.Cookies(); len(cleared) != 1 || cleared[0].MaxAge >= 0 {
		t.Errorf("logout cookies = %+v, want the cookie cleared", cleared)
	}
	resp = request(t, http.MethodGet, ts.URL+"/state", http.Header{"Cookie": {c.Name + "=" + c.Value}}, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("state after logout status = %d, want 401", resp.StatusCode)
	}
}

func TestHTTP_LoginRefusalsAndCookieLimits(t *testing.T) {
	t.Parallel()

	s := runSession(t, broker.Config{}, true)
	ts := httpServer(t, s.b, transport.HTTPConfig{CookieTTL: 150 * time.Millisecond, PublicURL: "http://127.0.0.1"})

	resp := request(t, http.MethodPost, ts.URL+"/login", auth("wrong"), "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized || len(resp.Cookies()) != 0 {
		t.Errorf("login with a wrong token = %d with %d cookies, want 401 and none", resp.StatusCode, len(resp.Cookies()))
	}

	resp = request(t, http.MethodPost, ts.URL+"/login", auth(viewerToken), "")
	_ = resp.Body.Close()
	c := resp.Cookies()[0]
	if c.Secure {
		t.Error("cookie is Secure under a plain-HTTP public URL")
	}
	cookie := http.Header{"Cookie": {c.Name + "=" + c.Value}}

	// Another broker process never issued this cookie.
	other := httpServer(t, s.b, transport.HTTPConfig{})
	resp = request(t, http.MethodGet, other.URL+"/state", cookie, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("cookie at another broker status = %d, want 401", resp.StatusCode)
	}

	// The cookie expires.
	time.Sleep(200 * time.Millisecond)
	resp = request(t, http.MethodGet, ts.URL+"/state", cookie, "")
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expired cookie status = %d, want 401", resp.StatusCode)
	}
}
