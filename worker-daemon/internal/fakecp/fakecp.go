// Package fakecp is a fake control plane for tests. It serves worker
// API protocol 1 as Sortie does under environment.kind remote (the same
// routes, bodies, header checks and 400, 401 and 409 answers), and the
// five data-plane flows of each job it offers, each behind that job's
// flow token. It records every request in order.
package fakecp

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/protocol"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/stdio"
)

// Event is one thing the fake saw.
type Event struct {
	Kind    string // checkin, poll, heartbeat, report, seed, bundle, usage, stdio, egress, mcp
	Job     string
	Attempt int
	Status  int
	At      time.Time
	Body    []byte
}

// Job is a job the fake offers.
type Job struct {
	ID       string
	Attempt  int
	Requires protocol.Requires
	Run      protocol.Run
	// Seed is the bundle served at the run's seed path.
	Seed []byte
	// StdioInput is sent to the agent, one message each, once the
	// stdio stream opens.
	StdioInput []string
	// KeepInputOpen leaves the agent's input open after StdioInput:
	// no C frame is sent.
	KeepInputOpen bool
}

type lease struct {
	worker  string
	expires time.Time
	ended   bool
	lost    bool
}

// Fake is a fake control plane.
type Fake struct {
	Server *httptest.Server
	// LeaseMS is the lease; heartbeat_ms is a third of it.
	LeaseMS int64
	// IdentityHeader names the worker; default X-Sortie-Worker-Identity.
	IdentityHeader string

	mu       sync.Mutex
	events   []Event
	pending  []Job
	jobs     map[string]Job
	tokens   map[string]map[string]string // job key → flow → token
	leases   map[string]*lease
	workers  map[string]protocol.Declaration
	answer   map[string]int // job key → status the heartbeat answers
	attempts map[string]*attemptData
	// down answers every request 502, as a proxy in front of a Sortie
	// that is restarting does.
	down bool
}

// attemptData is what the worker sent on an attempt's flows.
type attemptData struct {
	stdout, stderr bytes.Buffer
	exit           *int
	streamOpened   bool
	streamEnded    bool
	// stream is the open stdio stream; breaks counts the streams the
	// fake broke, as a Sortie that restarts breaks them.
	stream *stdio.Conn
	breaks int
	result *Result
	usage  []protocol.Usage
}

// Result is what the worker returned on the bundles flow.
type Result struct {
	// Bundle is the bundle part; nil when the worker left it out.
	Bundle []byte
	// PR is the pr part; nil when the worker left it out.
	PR []byte
	// Status is the status part; nil when the worker left it out.
	Status []byte
	// Criteria is the criteria part; nil when the worker left it out.
	Criteria []byte
	// ContentType is the request's media type.
	ContentType string
}

func key(job string, attempt int) string { return job + "#" + strconv.Itoa(attempt) }

// New starts a fake on loopback.
func New() *Fake {
	f := &Fake{LeaseMS: 3000, jobs: map[string]Job{}, tokens: map[string]map[string]string{}, leases: map[string]*lease{},
		workers: map[string]protocol.Declaration{}, answer: map[string]int{}, attempts: map[string]*attemptData{}}
	f.Server = httptest.NewServer(f)
	return f
}

// Close stops the fake.
func (f *Fake) Close() { f.Server.Close() }

// URL is the control plane's URL.
func (f *Fake) URL() string { return f.Server.URL }

func (f *Fake) record(e Event) {
	e.At = time.Now()
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

// Events returns what the fake saw, in order.
func (f *Fake) Events() []Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// Count returns how many events of kind for the attempt (any job when
// job is "") the fake saw.
func (f *Fake) Count(kind, job string, attempt int) int {
	n := 0
	for _, e := range f.Events() {
		if e.Kind == kind && (job == "" || (e.Job == job && e.Attempt == attempt)) {
			n++
		}
	}
	return n
}

// Declaration returns the latest declaration of a worker.
func (f *Fake) Declaration(worker string) (protocol.Declaration, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.workers[worker]
	return d, ok
}

// Beats returns the bodies of the attempt's accepted heartbeats, in order.
func (f *Fake) Beats(job string, attempt int) []protocol.Beat {
	var out []protocol.Beat
	for _, e := range f.Events() {
		if e.Kind != "heartbeat" || e.Job != job || e.Attempt != attempt || e.Status != http.StatusOK {
			continue
		}
		var b protocol.Beat
		_ = json.Unmarshal(e.Body, &b)
		out = append(out, b)
	}
	return out
}

// Offer queues a job.
func (f *Fake) Offer(j Job) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, j)
}

// Restart breaks the attempt's open stdio stream as a Sortie that stops
// breaks it, without the C frame, and answers every request 502 until
// [Fake.Up]; the next stream is then accepted, and its input closed
// after StdioInput whatever KeepInputOpen says. The lease and the tokens
// stay, as a restarted Sortie restores them.
func (f *Fake) Restart(job string, attempt int) {
	f.mu.Lock()
	a := f.data(key(job, attempt))
	c := a.stream
	a.stream, a.streamOpened = nil, false
	a.breaks++
	f.down = true
	f.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
}

// Up ends a [Fake.Restart]: requests are served again, and each lease
// lasts LeaseMS from now, as a restarted Sortie restores it.
func (f *Fake) Up() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = false
	for _, l := range f.leases {
		if !l.ended && !l.lost {
			l.expires = time.Now().Add(time.Duration(f.LeaseMS) * time.Millisecond)
		}
	}
}

// Streams counts the stdio streams the attempt opened.
func (f *Fake) Streams(job string, attempt int) int {
	n := 0
	for _, e := range f.Events() {
		if e.Kind == "stdio" && e.Job == job && e.Attempt == attempt && e.Status == http.StatusSwitchingProtocols {
			n++
		}
	}
	return n
}

// AnswerHeartbeat makes every later heartbeat for the attempt answer
// status, such as 409; the lease then counts as lost.
func (f *Fake) AnswerHeartbeat(job string, attempt, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answer[key(job, attempt)] = status
}

// Tokens returns the attempt's flow tokens.
func (f *Fake) Tokens(job string, attempt int) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]string{}
	maps.Copy(out, f.tokens[key(job, attempt)])
	return out
}

// data returns the attempt's record; f.mu is held.
func (f *Fake) data(k string) *attemptData {
	a := f.attempts[k]
	if a == nil {
		a = &attemptData{}
		f.attempts[k] = a
	}
	return a
}

// StdioOutput is what the agent wrote to its standard output, from the
// stream's O frames.
func (f *Fake) StdioOutput(job string, attempt int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data(key(job, attempt)).stdout.String()
}

// StdioError is what the agent wrote to its standard error, from the
// stream's E frames.
func (f *Fake) StdioError(job string, attempt int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.data(key(job, attempt)).stderr.String()
}

// Exit is the exit status of the stream's X frame, and whether one
// came.
func (f *Fake) Exit(job string, attempt int) (int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if x := f.data(key(job, attempt)).exit; x != nil {
		return *x, true
	}
	return 0, false
}

// StreamBroken reports a stdio stream that ended before its X frame.
func (f *Fake) StreamBroken(job string, attempt int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.data(key(job, attempt))
	return a.streamEnded && a.exit == nil
}

// Result is what the worker returned for the attempt, and whether it
// returned anything.
func (f *Fake) Result(job string, attempt int) (Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.data(key(job, attempt)).result; r != nil {
		return *r, true
	}
	return Result{}, false
}

// Usage is the latest usage the worker reported for the attempt: its
// totals so far.
func (f *Fake) Usage(job string, attempt int) (protocol.Usage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	us := f.data(key(job, attempt)).usage
	if len(us) == 0 {
		return protocol.Usage{}, false
	}
	return us[len(us)-1], true
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func problem(w http.ResponseWriter, code int, kind, msg string) {
	reply(w, code, map[string]any{"error": map[string]any{"code": kind, "message": msg}})
}

// ServeHTTP routes the worker API, the flows, and the egress proxy.
func (f *Fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	down := f.down
	f.mu.Unlock()
	if down {
		http.Error(w, "the control plane is restarting", http.StatusBadGateway)
		f.record(Event{Kind: "down", Status: http.StatusBadGateway})
		return
	}
	if r.Method == http.MethodConnect || r.URL.IsAbs() {
		f.egress(w, r)
		return
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/v1/workers/"):
		f.workerAPI(w, r)
	case strings.HasPrefix(r.URL.Path, "/flows/"):
		f.flow(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *Fake) identity() string {
	if f.IdentityHeader != "" {
		return f.IdentityHeader
	}
	return protocol.DefaultIdentityHeader
}

func (f *Fake) workerAPI(w http.ResponseWriter, r *http.Request) {
	if v := r.Header.Get(protocol.ProtocolHeader); v != "1" {
		body := map[string]any{"error": map[string]any{"code": "unsupported_protocol", "message": "send " + protocol.ProtocolHeader + ": 1"}, "supported": []int{1}}
		reply(w, http.StatusBadRequest, body)
		f.record(Event{Kind: "bad-protocol", Status: 400})
		return
	}
	id := r.Header.Get(f.identity())
	if id == "" {
		problem(w, http.StatusUnauthorized, "missing_identity", "no worker named")
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/workers/")
	switch path {
	case "checkin":
		f.checkIn(w, id, body)
		return
	case "poll":
		f.poll(w, id)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) != 5 || parts[0] != "jobs" || parts[2] != "attempts" {
		http.NotFound(w, r)
		return
	}
	job := parts[1]
	attempt, err := strconv.Atoi(parts[3])
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid_attempt", "attempt must be an integer")
		return
	}
	switch parts[4] {
	case "heartbeat":
		f.heartbeat(w, id, job, attempt, body)
	case "report":
		f.report(w, id, job, attempt, body)
	default:
		http.NotFound(w, r)
	}
}

// checkIn validates a declaration as Sortie does: every field present,
// non-null, of its type, and no negative number.
func (f *Fake) checkIn(w http.ResponseWriter, id string, body []byte) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		problem(w, http.StatusBadRequest, "invalid_checkin", "check-in body is not a JSON object")
		f.record(Event{Kind: "checkin", Status: 400, Body: body})
		return
	}
	var d protocol.Declaration
	var missing, invalid []string
	field := func(obj map[string]json.RawMessage, k, path string, dst any) {
		v, ok := obj[k]
		if !ok || string(bytes.TrimSpace(v)) == "null" {
			missing = append(missing, path)
			return
		}
		if err := json.Unmarshal(v, dst); err != nil {
			invalid = append(invalid, path)
		}
	}
	field(raw, "os", "os", &d.OS)
	field(raw, "arch", "arch", &d.Arch)
	field(raw, "kvm", "kvm", &d.KVM)
	field(raw, "nested", "nested", &d.Nested)
	field(raw, "backends", "backends", &d.Backends)
	field(raw, "run_slots", "run_slots", &d.RunSlots)
	field(raw, "running", "running", &d.Running)
	var free map[string]json.RawMessage
	field(raw, "free", "free", &free)
	if free != nil {
		field(free, "cpu", "free.cpu", &d.Free.CPU)
		field(free, "mem_gib", "free.mem_gib", &d.Free.MemGiB)
		field(free, "disk_gib", "free.disk_gib", &d.Free.DiskGiB)
	}
	for _, n := range []struct {
		p string
		v float64
	}{{"run_slots", float64(d.RunSlots)}, {"running", float64(d.Running)}, {"free.cpu", d.Free.CPU}, {"free.mem_gib", d.Free.MemGiB}, {"free.disk_gib", d.Free.DiskGiB}} {
		if n.v < 0 {
			invalid = append(invalid, n.p)
		}
	}
	// Used and unmanaged are optional, as Sortie ignores what it does not know.
	if v, ok := raw["used"]; ok {
		_ = json.Unmarshal(v, &d.Used)
	}
	if v, ok := raw["unmanaged"]; ok {
		_ = json.Unmarshal(v, &d.Unmanaged)
	}
	if len(missing) > 0 || len(invalid) > 0 {
		reply(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "invalid_checkin", "message": "bad declaration"},
			"missing": append([]string{}, missing...), "invalid": append([]string{}, invalid...)})
		f.record(Event{Kind: "checkin", Status: 400, Body: body})
		return
	}
	f.mu.Lock()
	f.workers[id] = d
	f.mu.Unlock()
	reply(w, http.StatusOK, protocol.Timings{HeartbeatMS: f.LeaseMS / 3, LeaseMS: f.LeaseMS})
	f.record(Event{Kind: "checkin", Status: 200, Body: body})
}

func newToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// wireOffer is an offer with its tokens in clear, as the control plane
// sends it.
type wireOffer struct {
	Job   protocol.Job                 `json:"job"`
	Lease protocol.Lease               `json:"lease"`
	Run   protocol.Run                 `json:"run"`
	Flows map[string]map[string]string `json:"flows"`
}

func (f *Fake) poll(w http.ResponseWriter, id string) {
	f.mu.Lock()
	if _, ok := f.workers[id]; !ok {
		f.mu.Unlock()
		problem(w, http.StatusConflict, "not_checked_in", "check in before polling")
		f.record(Event{Kind: "poll", Status: 409})
		return
	}
	if len(f.pending) == 0 {
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		f.record(Event{Kind: "poll", Status: 204})
		return
	}
	j := f.pending[0]
	f.pending = f.pending[1:]
	k := key(j.ID, j.Attempt)
	f.jobs[k] = j
	toks := map[string]string{}
	flowsOut := map[string]map[string]string{}
	for _, name := range protocol.Flows {
		toks[name] = newToken()
		u := f.Server.URL + "/flows/" + name
		if name == protocol.FlowEgress {
			u = f.Server.URL
		}
		flowsOut[name] = map[string]string{"url": u, "token": toks[name]}
	}
	f.tokens[k] = toks
	exp := time.Now().Add(time.Duration(f.LeaseMS) * time.Millisecond)
	f.leases[k] = &lease{worker: id, expires: exp}
	run := j.Run
	if run.SeedPath == "" {
		run.SeedPath = "/jobs/" + j.ID + "/attempts/" + strconv.Itoa(j.Attempt) + "/seed"
	}
	stored := j
	stored.Run = run
	f.jobs[k] = stored
	f.mu.Unlock()
	reply(w, http.StatusOK, wireOffer{
		Job:   protocol.Job{ID: j.ID, Attempt: j.Attempt, Issue: protocol.Issue{ID: "id-" + j.ID, Identifier: j.ID, Title: "fake " + j.ID}, Requires: j.Requires},
		Lease: protocol.Lease{Expires: exp.UTC()}, Run: run, Flows: flowsOut,
	})
	f.record(Event{Kind: "poll", Status: 200, Job: j.ID, Attempt: j.Attempt})
}

// live returns the attempt's lease when id holds it live.
func (f *Fake) live(id, job string, attempt int) *lease {
	l := f.leases[key(job, attempt)]
	if l == nil || l.ended || l.lost || l.worker != id || !time.Now().Before(l.expires) {
		return nil
	}
	return l
}

func (f *Fake) heartbeat(w http.ResponseWriter, id, job string, attempt int, body []byte) {
	k := key(job, attempt)
	f.mu.Lock()
	if code, ok := f.answer[k]; ok {
		if l := f.leases[k]; l != nil && code == http.StatusConflict {
			l.lost = true
			delete(f.tokens, k)
		}
		f.mu.Unlock()
		problem(w, code, "lease_lost", "the control plane ended the attempt")
		f.record(Event{Kind: "heartbeat", Job: job, Attempt: attempt, Status: code})
		return
	}
	l := f.live(id, job, attempt)
	if l == nil {
		f.mu.Unlock()
		problem(w, http.StatusConflict, "lease_lost", "no live lease")
		f.record(Event{Kind: "heartbeat", Job: job, Attempt: attempt, Status: 409})
		return
	}
	l.expires = time.Now().Add(time.Duration(f.LeaseMS) * time.Millisecond)
	exp := l.expires
	f.mu.Unlock()
	reply(w, http.StatusOK, protocol.Lease{Expires: exp.UTC()})
	f.record(Event{Kind: "heartbeat", Job: job, Attempt: attempt, Status: 200, Body: body})
}

func (f *Fake) report(w http.ResponseWriter, id, job string, attempt int, body []byte) {
	var r struct {
		Outcome *string `json:"outcome"`
		Reason  *string `json:"reason"`
		Machine *string `json:"machine"`
	}
	if err := json.Unmarshal(body, &r); err != nil || r.Outcome == nil || (*r.Outcome != "succeeded" && *r.Outcome != "failed") ||
		r.Machine == nil || (*r.Machine != "running" && *r.Machine != "stopped" && *r.Machine != "deleted") {
		problem(w, http.StatusBadRequest, "invalid_report", `report body must be {"outcome", "reason", "machine"}`)
		f.record(Event{Kind: "report", Job: job, Attempt: attempt, Status: 400, Body: body})
		return
	}
	f.mu.Lock()
	l := f.live(id, job, attempt)
	if l == nil {
		f.mu.Unlock()
		problem(w, http.StatusConflict, "lease_lost", "no live lease")
		f.record(Event{Kind: "report", Job: job, Attempt: attempt, Status: 409, Body: body})
		return
	}
	l.ended = true
	delete(f.tokens, key(job, attempt))
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
	f.record(Event{Kind: "report", Job: job, Attempt: attempt, Status: 204, Body: body})
}

// authorized checks the bearer token of a flow request.
func (f *Fake) authorized(flow, job string, attempt int, header string) bool {
	want := ""
	f.mu.Lock()
	if t := f.tokens[key(job, attempt)]; t != nil {
		want = t[flow]
	}
	f.mu.Unlock()
	got, ok := strings.CutPrefix(header, "Bearer ")
	return ok && want != "" && got == want
}

// tokenJob finds the attempt a token of flow belongs to.
func (f *Fake) tokenJob(flow, header string) (string, int, bool) {
	got, ok := strings.CutPrefix(header, "Bearer ")
	if !ok {
		return "", 0, false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, t := range f.tokens {
		if t[flow] == got {
			j := f.jobs[k]
			return j.ID, j.Attempt, true
		}
	}
	return "", 0, false
}

// flow serves /flows/{flow}/jobs/{job}/attempts/{n}[/rest] as Sortie's
// data plane does: each request needs the protocol header and its
// flow's live token for that attempt.
func (f *Fake) flow(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/flows/"), "/", 6)
	if len(parts) < 5 || parts[1] != "jobs" || parts[3] != "attempts" {
		http.NotFound(w, r)
		return
	}
	flow, job := parts[0], parts[2]
	attempt, err := strconv.Atoi(parts[4])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	rest := ""
	if len(parts) == 6 {
		rest = parts[5]
	}
	if r.Header.Get(protocol.ProtocolHeader) != "1" {
		problem(w, http.StatusBadRequest, "unsupported_protocol", "send "+protocol.ProtocolHeader+": 1")
		f.record(Event{Kind: "bad-protocol", Job: job, Attempt: attempt, Status: 400})
		return
	}
	if !f.authorized(flow, job, attempt, r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="sortie-job"`)
		problem(w, http.StatusUnauthorized, "invalid_token", "the request carries no live "+flow+" token of this job's attempt")
		f.record(Event{Kind: flow, Job: job, Attempt: attempt, Status: 401})
		return
	}
	k := key(job, attempt)
	switch {
	case flow == protocol.FlowBundles && r.Method == http.MethodGet && rest == "seed":
		f.mu.Lock()
		seed := f.jobs[k].Seed
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-git-bundle")
		w.Header().Set("Content-Length", strconv.Itoa(len(seed)))
		_, _ = w.Write(seed)
		f.record(Event{Kind: "seed", Job: job, Attempt: attempt, Status: 200})
	case flow == protocol.FlowBundles && r.Method == http.MethodPost && rest == "result":
		f.result(w, r, job, attempt)
	case flow == protocol.FlowUsage && r.Method == http.MethodPost && rest == "":
		f.usageReport(w, r, job, attempt)
	case flow == protocol.FlowStdio && r.Method == http.MethodGet && rest == "":
		f.stdio(w, r, job, attempt)
	case flow == protocol.FlowMCP:
		b, _ := io.ReadAll(r.Body)
		reply(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 1, "result": map[string]any{"content": []any{map[string]any{"type": "text", "text": "tool ok"}}}})
		f.record(Event{Kind: "mcp", Job: job, Attempt: attempt, Status: 200, Body: b})
	default:
		http.NotFound(w, r)
	}
}

// result takes the multipart result: the part bundle, left out when the
// machine has no issue branch, and the optional part pr, at most 64 KiB.
// Any other part, two bundles, or a second result is refused.
func (f *Fake) result(w http.ResponseWriter, r *http.Request, job string, attempt int) {
	refuse := func(code int, msg string) {
		problem(w, code, "invalid_result", msg)
		f.record(Event{Kind: "result", Job: job, Attempt: attempt, Status: code, Body: []byte(msg)})
	}
	mr, err := r.MultipartReader()
	if err != nil {
		refuse(http.StatusBadRequest, "the result must be multipart/form-data with the parts bundle and pr")
		return
	}
	res := Result{ContentType: r.Header.Get("Content-Type")}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			refuse(http.StatusBadRequest, "the result body cannot be read: "+err.Error())
			return
		}
		b, err := io.ReadAll(part)
		if err != nil {
			refuse(http.StatusBadRequest, "the result body cannot be read: "+err.Error())
			return
		}
		switch part.FormName() {
		case protocol.ResultBundlePart:
			if res.Bundle != nil {
				refuse(http.StatusBadRequest, "the result carries two bundles")
				return
			}
			res.Bundle = b
		case protocol.ResultPRPart:
			if len(b) > protocol.MaxPRFile {
				refuse(http.StatusRequestEntityTooLarge, "the pr part exceeds 64 KiB")
				return
			}
			res.PR = b
		case protocol.ResultStatusPart:
			if len(b) > protocol.MaxStatusFile {
				refuse(http.StatusRequestEntityTooLarge, "the status part exceeds 4 KiB")
				return
			}
			res.Status = b
		case protocol.ResultCriteriaPart:
			if len(b) > protocol.MaxCriteriaFile {
				refuse(http.StatusRequestEntityTooLarge, "the criteria part exceeds 64 KiB")
				return
			}
			res.Criteria = b
		default:
			refuse(http.StatusBadRequest, fmt.Sprintf("unknown result part %q; valid: bundle, pr, status, criteria", part.FormName()))
			return
		}
	}
	f.mu.Lock()
	a := f.data(key(job, attempt))
	if a.result != nil {
		f.mu.Unlock()
		refuse(http.StatusConflict, "the attempt's result was already returned")
		return
	}
	a.result = &res
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
	f.record(Event{Kind: "result", Job: job, Attempt: attempt, Status: 204, Body: res.Bundle})
}

// usageReport takes a usage report: every field optional, counts not
// negative, measured true when absent.
func (f *Fake) usageReport(w http.ResponseWriter, r *http.Request, job string, attempt int) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 4<<10))
	var body struct {
		InputTokens      *int64  `json:"input_tokens"`
		OutputTokens     *int64  `json:"output_tokens"`
		CacheReadTokens  *int64  `json:"cache_read_tokens"`
		CacheWriteTokens *int64  `json:"cache_write_tokens"`
		Model            *string `json:"model"`
		Measured         *bool   `json:"measured"`
	}
	if err := json.Unmarshal(b, &body); err != nil {
		problem(w, http.StatusBadRequest, "invalid_usage", "the usage body must be a JSON object")
		f.record(Event{Kind: "usage", Job: job, Attempt: attempt, Status: 400, Body: b})
		return
	}
	u := protocol.Usage{Measured: body.Measured == nil || *body.Measured}
	if body.Model != nil {
		u.Model = *body.Model
	}
	for _, c := range []struct {
		src *int64
		dst *int64
	}{{body.InputTokens, &u.InputTokens}, {body.OutputTokens, &u.OutputTokens}, {body.CacheReadTokens, &u.CacheReadTokens}, {body.CacheWriteTokens, &u.CacheWriteTokens}} {
		if c.src == nil {
			continue
		}
		if *c.src < 0 {
			problem(w, http.StatusBadRequest, "invalid_usage", "a count must not be negative")
			f.record(Event{Kind: "usage", Job: job, Attempt: attempt, Status: 400, Body: b})
			return
		}
		*c.dst = *c.src
	}
	f.mu.Lock()
	a := f.data(key(job, attempt))
	a.usage = append(a.usage, u)
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
	f.record(Event{Kind: "usage", Job: job, Attempt: attempt, Status: 204, Body: b})
}

// stdio upgrades to sortie-stdio/1, sends the job's StdioInput as I
// frames and then C, unless KeepInputOpen, and reads the worker's O, E
// and X frames. A stream that ends before X is broken.
func (f *Fake) stdio(w http.ResponseWriter, r *http.Request, job string, attempt int) {
	k := key(job, attempt)
	f.mu.Lock()
	a := f.data(k)
	opened := a.streamOpened

	f.mu.Unlock()
	if opened {
		problem(w, http.StatusConflict, "stdio_open", "the attempt's stdio stream was already opened")
		return
	}
	c, err := stdio.Accept(w, r)
	if err != nil {
		f.record(Event{Kind: "stdio", Job: job, Attempt: attempt, Status: http.StatusUpgradeRequired})
		return
	}
	defer func() { _ = c.Close() }()
	f.mu.Lock()
	j := f.jobs[k]
	a.streamOpened = true
	a.stream = c
	keepOpen := j.KeepInputOpen && a.breaks == 0
	f.mu.Unlock()
	f.record(Event{Kind: "stdio", Job: job, Attempt: attempt, Status: 101})
	go func() {
		for _, in := range j.StdioInput {
			if _, err := (stdio.Writer{C: c, Type: stdio.Stdin}).Write([]byte(in)); err != nil {
				return
			}
		}
		if !keepOpen {
			_ = c.WriteFrame(stdio.StdinClosed, nil)
		}
	}()
	defer func() {
		f.mu.Lock()
		a.streamEnded = true
		f.mu.Unlock()
		f.record(Event{Kind: "stdio-end", Job: job, Attempt: attempt})
	}()
	for {
		typ, payload, err := c.ReadFrame()
		if err != nil {
			return
		}
		f.mu.Lock()
		switch typ {
		case stdio.Stdout:
			a.stdout.Write(payload)
		case stdio.Stderr:
			a.stderr.Write(payload)
		case stdio.Exit:
			if status, err := stdio.ExitStatus(payload); err == nil {
				a.exit = &status
			}
		}
		f.mu.Unlock()
		f.record(Event{Kind: "stdio-" + string(typ), Job: job, Attempt: attempt, Body: payload})
		if typ == stdio.Exit {
			return
		}
		if typ != stdio.Stdout && typ != stdio.Stderr {
			return
		}
	}
}

// egress is the egress flow: an HTTP proxy that admits a request with
// the job's egress token in Proxy-Authorization. Inside a CONNECT
// tunnel it answers HTTP itself, as a model API would.
func (f *Fake) egress(w http.ResponseWriter, r *http.Request) {
	job, attempt, ok := f.tokenJob(protocol.FlowEgress, r.Header.Get("Proxy-Authorization"))
	if !ok {
		w.Header().Set("Proxy-Authenticate", `Bearer realm="sortie-job"`)
		http.Error(w, "egress needs a job token", http.StatusProxyAuthRequired)
		f.record(Event{Kind: "egress", Status: 407})
		return
	}
	if r.Method != http.MethodConnect {
		b, _ := io.ReadAll(r.Body)
		reply(w, http.StatusOK, map[string]any{"model": "fake", "usage": map[string]int{"input_tokens": 3}})
		f.record(Event{Kind: "egress", Job: job, Attempt: attempt, Status: 200, Body: append([]byte(r.Method+" "+r.URL.String()+"\n"), b...)})
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "no tunnel", http.StatusInternalServerError)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = rw.WriteString("HTTP/1.1 200 Connection established\r\n\r\n")
	_ = rw.Flush()
	f.record(Event{Kind: "egress", Job: job, Attempt: attempt, Status: 200, Body: []byte("CONNECT " + r.Host)})
	br := rw.Reader
	for {
		req, err := http.ReadRequest(br)
		if err != nil {
			return
		}
		b, _ := io.ReadAll(req.Body)
		f.record(Event{Kind: "egress", Job: job, Attempt: attempt, Status: 200, Body: append([]byte(req.Method+" "+r.Host+req.URL.Path+"\n"), b...)})
		resp := `{"model":"fake","usage":{"input_tokens":3}}`
		if _, err := fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(resp), resp); err != nil {
			return
		}
	}
}

// Addr is the fake's listening address.
func (f *Fake) Addr() net.Addr { return f.Server.Listener.Addr() }
