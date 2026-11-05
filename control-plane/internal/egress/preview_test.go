package egress

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	previewDomain = "preview.p1.example.org"
	previewName   = "ons-7." + previewDomain
	reviewKey     = "the-previews-review-key"
)

// previewHarness is the gate with the previews of the kit's host: one
// preview, a stand-in for a ui story's workbench on the gate's loopback,
// that answers its home and its API and says what it was sent.
type previewHarness struct {
	*harness
	preview *httptest.Server
	routes  string
}

func newPreviewHarness(t *testing.T) *previewHarness {
	t.Helper()
	p := &previewHarness{}
	p.preview = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			_, _ = io.WriteString(w, "<html>the preview's home</html>")
			return
		}
		_, _ = fmt.Fprintf(w, "host=%s key=%s job=%s proxy=%s path=%s", r.Host, r.Header.Get(ReviewKeyHeader),
			r.Header.Get(ReviewJobHeader), r.Header.Get("Proxy-Authorization"), r.URL.RequestURI())
	}))
	t.Cleanup(p.preview.Close)
	u, _ := url.Parse(p.preview.URL)
	p.routes = filepath.Join(t.TempDir(), "front-routes")
	if err := os.WriteFile(p.routes, []byte("ons-9."+previewDomain+" 1\n"+previewName+" "+u.Port()+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.harness = newHarnessWith(t, func(h *harness, cfg *Config) {
		cfg.Previews = &Previews{Domain: previewDomain, RoutesFile: p.routes, ReviewKeyFile: writeSecret(t, "review-key", reviewKey)}
	})
	return p
}

// tunnel opens a CONNECT for host as a worker's egress forwarder does,
// claiming job ONS-7, and returns the answer's status and the open tunnel.
func (p *previewHarness) tunnel(host, authorization string) (int, net.Conn, *bufio.Reader) {
	p.t.Helper()
	u, _ := url.Parse(p.gate.URL)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		p.t.Fatal(err)
	}
	p.t.Cleanup(func() { _ = c.Close() })
	_, _ = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n%s: ONS-7\r\n\r\n", host, host, authorization, JobHeader)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		p.t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode, c, br
}

// get sends a GET for path inside an open tunnel, as a browser does, and
// returns the answer's status and body.
func get(t *testing.T, c net.Conn, br *bufio.Reader, path string, header string) (int, string) {
	t.Helper()
	if _, err := fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", path, previewName, header); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode, string(b)
}

// agent-platform/run-supervision#A UI is approved as a prototype, and the
// built story merges after its review, scenario The review machine reaches
// the preview: the review job's CONNECT for the preview's name, with its
// live egress token, opens the preview's home and its API from the gate's
// loopback, under the name it asked for, signed in by the review key, and
// never reaches nono.
func TestAJobReachesAPreviewByItsName(t *testing.T) {
	p := newPreviewHarness(t)
	code, c, br := p.tunnel(previewName+":80", "Bearer "+p.sortie.mint("ONS-7", Flow))
	if code != http.StatusOK {
		t.Fatalf("CONNECT %s: %d, want 200", previewName, code)
	}
	if status, body := get(t, c, br, "/", ""); status != http.StatusOK || body != "<html>the preview's home</html>" {
		t.Fatalf("home: %d %q", status, body)
	}
	// The same tunnel carries the page's API calls; a key the guest made
	// up is replaced by the gate's.
	status, body := get(t, c, br, "/api/projects?x=1", ReviewKeyHeader+": made-up\r\n"+ReviewJobHeader+": ONS-1\r\n")
	want := "host=" + previewName + " key=" + reviewKey + " job=ONS-7 proxy= path=/api/projects?x=1"
	if status != http.StatusOK || body != want {
		t.Fatalf("API: %d %q, want %q", status, body, want)
	}
	if seen := p.nono.seen(); len(seen) != 0 {
		t.Errorf("a preview's request reached nono: %v", seen)
	}
	port := strings.TrimPrefix(p.preview.URL, "http://127.0.0.1:")
	for _, line := range []string{
		`msg="preview tunnel" worker=p1 job=ONS-7 attempt=1 host=` + previewName + `:80 port=` + port,
		`msg="preview request" worker=p1 job=ONS-7 attempt=1 host=` + previewName + `:80 port=` + port + ` method=GET path=/api/projects status=200`,
	} {
		if !p.logged(line) {
			t.Errorf("the gate did not log %s in:\n%s", line, p.log)
		}
	}
}

// Only a job with a live egress token reaches a preview: no token, an ended
// one and another flow's are refused with 407 before the preview is
// reached, and each refusal is logged with the worker, the job and the host.
func TestAPreviewRefusesAJobWithoutALiveToken(t *testing.T) {
	p := newPreviewHarness(t)
	ended := p.sortie.mint("ONS-7", Flow)
	p.sortie.end(ended)
	for name, auth := range map[string]string{
		"no token":    "",
		"ended token": "Bearer " + ended,
		"mcp token":   "Bearer " + p.sortie.mint("ONS-7", "mcp"),
	} {
		if got, _, _ := p.tunnel(previewName+":80", auth); got != http.StatusProxyAuthRequired {
			t.Errorf("%s: %d, want 407", name, got)
		}
	}
	want := `msg="preview refused" worker=p1 job=ONS-7 host=` + previewName + `:80 status=407 reason="no live egress token"`
	if n := strings.Count(p.log.String(), want); n != 3 {
		t.Errorf("%d refusals logged as %s, want 3:\n%s", n, want, p.log)
	}
	if strings.Contains(p.log.String(), "preview tunnel") || len(p.nono.seen()) != 0 {
		t.Errorf("a refused CONNECT was carried:\n%s", p.log)
	}
}

func TestAPreviewNameTheGateDoesNotServe(t *testing.T) {
	p := newPreviewHarness(t)
	tok := p.sortie.mint("ONS-7", Flow)
	for host, status := range map[string]int{
		previewName + ":443":             http.StatusForbidden, // an address is http://NAME/
		"ons-8." + previewDomain + ":80": http.StatusNotFound,  // no preview deployed there
	} {
		if got, _, _ := p.tunnel(host, "Bearer "+tok); got != status {
			t.Errorf("CONNECT %s: %d, want %d", host, got, status)
		}
		if want := `msg="preview refused" worker=p1 job=ONS-7 host=` + host; !strings.Contains(p.log.String(), want) {
			t.Errorf("the refusal of %s is not logged: want %s in\n%s", host, want, p.log)
		}
	}
	if len(p.nono.seen()) != 0 {
		t.Errorf("a preview's name reached nono: %v", p.nono.seen())
	}
	// A deployed preview that does not answer is a 502 inside the tunnel.
	code, c, br := p.tunnel("ons-9."+previewDomain+":80", "Bearer "+tok)
	if code != http.StatusOK {
		t.Fatalf("CONNECT to a listed preview: %d", code)
	}
	if status, _ := get(t, c, br, "/", ""); status != http.StatusBadGateway {
		t.Errorf("a preview that does not answer: %d, want 502", status)
	}
}

// The kit checks that a preview answers through the route before it
// comments its address: Sortie's control token admits that check, which
// gets no review key.
func TestTheKitsCheckThatAPreviewAnswers(t *testing.T) {
	p := newPreviewHarness(t)
	code, c, br := p.tunnel(previewName+":80", "Bearer "+controlToken)
	if code != http.StatusOK {
		t.Fatalf("the kit's check: %d, want 200", code)
	}
	status, body := get(t, c, br, "/api/auth/mode", "")
	if status != http.StatusOK || !strings.Contains(body, "key= job= ") {
		t.Errorf("the check: %d %q, want no review key", status, body)
	}
	if !strings.Contains(p.log.String(), `msg="preview tunnel" worker=p1 job="" attempt=0 host=`+previewName+`:80`) || !strings.Contains(p.log.String(), "check=true") {
		t.Errorf("the check is not logged as the kit's:\n%s", p.log)
	}
	// The control token admits no other host.
	if status := p.connect("api.anthropic.com:443", "Bearer "+controlToken); status != http.StatusProxyAuthRequired {
		t.Errorf("the control token on the egress flow: %d, want 407", status)
	}
}

func TestPreviewsConfig(t *testing.T) {
	control := writeSecret(t, "control-token", controlToken)
	pass := writeSecret(t, "nono-pass", "pass")
	for name, pv := range map[string]*Previews{
		"no domain":      {RoutesFile: "/x/routes"},
		"a bare name":    {Domain: "preview", RoutesFile: "/x/routes"},
		"a wildcard":     {Domain: "*.p1.example.org", RoutesFile: "/x/routes"},
		"no routes file": {Domain: previewDomain},
	} {
		if _, err := New(Config{Sortie: "http://127.0.0.1:7680", ControlTokenFile: control, Nono: "127.0.0.1:7690", NonoPassFile: pass, Previews: pv}, slog.New(slog.DiscardHandler)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// logged waits a little for line in the gate's log: a request is logged
// once its answer is written, which the client may read first.
func (p *previewHarness) logged(line string) bool {
	for range 100 {
		if strings.Contains(p.log.String(), line) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
