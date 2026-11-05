package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	sortieToken = "control-token"
	githubToken = "kit-github-token"
	kitLogin    = "ortus-kit"
	homelabPR   = "ortusnova/homelab#41"
	headA       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	headB       = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	headC       = "cccccccccccccccccccccccccccccccccccccccc"
)

var t0 = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

// fakeSortie serves the supervisor's actions in GET /api/v1/state and
// takes the fix runs the kit asks for, as Sortie's control API does.
type fakeSortie struct {
	*httptest.Server
	mu      sync.Mutex
	actions []map[string]any
	refuse  string // the error code of the next fix, when set
	fixes   []map[string]string
	parked  []string // the identifiers of the parked stories
	// place, when set, is the matcher: it takes each fix run and names
	// the worker that runs its job, or "" when none may.
	place func(fix map[string]string) string
	// placed is the worker each fix run's job went to, in order.
	placed []string
}

func newFakeSortie(t *testing.T) *fakeSortie {
	f := &fakeSortie{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/state", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		parked := []map[string]string{}
		for _, id := range f.parked {
			parked = append(parked, map[string]string{"issue_identifier": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"running": []any{}, "parked": parked, "supervisor_actions": f.actions})
	})
	mux.HandleFunc("POST /api/v1/{identifier}/fix", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+sortieToken {
			http.Error(w, `{"error":{"code":"unauthorized"}}`, http.StatusUnauthorized)
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.refuse != "" {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": f.refuse, "message": "not now"}})
			f.refuse = ""
			return
		}
		body["identifier"] = r.PathValue("identifier")
		f.fixes = append(f.fixes, body)
		if f.place != nil {
			f.placed = append(f.placed, f.place(body))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"action": "fix_run", "issue_identifier": body["identifier"], "pull_request": homelabPR})
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// request is the supervisor's POST /api/v1/ONS-41/live-proof, as Sortie
// records it.
func (f *fakeSortie) request(id int64, evidence string, at time.Time) {
	f.requestWith(id, evidence, "", at)
}

// requestWith is the supervisor's request naming the job profile
// profile, which Sortie records as the action's profile; "" names none,
// which Sortie serves as null.
func (f *fakeSortie) requestWith(id int64, evidence, profile string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ident, pr := "ONS-41", homelabPR
	var named any
	if profile != "" {
		named = profile
	}
	f.actions = append(f.actions, map[string]any{
		"id": id, "action": "live_proof", "route": "/api/v1/ONS-41/live-proof", "issue_identifier": ident,
		"evidence": evidence, "result": "done", "status": 200, "pull_request": pr, "profile": named, "at": at,
	})
}

// fakeForge is GitHub for one pull request: its head and state, its
// comments, and the statuses the kit sets.
type fakeForge struct {
	*httptest.Server
	mu       sync.Mutex
	head     string
	state    string
	comments []map[string]any
	statuses []map[string]string
}

func newFakeForge(t *testing.T) *fakeForge {
	f := &fakeForge{head: headA, state: "open"}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+githubToken {
				http.Error(w, "Bad credentials", http.StatusUnauthorized)
				return
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			h(w, r)
		}
	}
	mux.HandleFunc("GET /user", auth(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"login": kitLogin})
	}))
	mux.HandleFunc("GET /repos/ortusnova/homelab/pulls/41", auth(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"state": f.state, "merged": f.state == "closed",
			"head": map[string]string{"sha": f.head, "ref": "sortie/ONS-41"}})
	}))
	mux.HandleFunc("GET /repos/ortusnova/homelab/issues/41/comments", auth(func(w http.ResponseWriter, r *http.Request) {
		since, _ := time.Parse(time.RFC3339, r.URL.Query().Get("since"))
		out := []map[string]any{}
		for _, c := range f.comments {
			if !c["created_at"].(time.Time).Before(since) {
				out = append(out, c)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("POST /repos/ortusnova/homelab/statuses/{sha}", auth(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["sha"] = r.PathValue("sha")
		f.statuses = append(f.statuses, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *fakeForge) comment(login, body string, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := len(f.comments) + 1
	f.comments = append(f.comments, map[string]any{
		"body": body, "created_at": at, "html_url": "https://github.com/ortusnova/homelab/pull/41#issuecomment-" + string(rune('0'+n)),
		"user": map[string]string{"login": login},
	})
}

func (f *fakeForge) set(head, state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head, f.state = head, state
}

// last is the newest status the kit set, and how many it set in all.
func (f *fakeForge) last() (map[string]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		return nil, 0
	}
	return f.statuses[len(f.statuses)-1], len(f.statuses)
}

type scenario struct {
	t      *testing.T
	sortie *fakeSortie
	forge  *fakeForge
	s      *Syncer
	clock  time.Time
	log    *strings.Builder
}

func newScenario(t *testing.T) *scenario {
	sc := &scenario{t: t, sortie: newFakeSortie(t), forge: newFakeForge(t), clock: t0, log: &strings.Builder{}}
	sc.s = &Syncer{
		Sortie: sc.sortie.URL, SortieToken: sortieToken, GitHub: sc.forge.URL, GitHubToken: githubToken,
		Dir: filepath.Join(t.TempDir(), "live-proofs"), Client: http.DefaultClient,
		Log: slog.New(slog.NewTextHandler(sc.log, nil)), Now: func() time.Time { return sc.clock },
	}
	return sc
}

func (sc *scenario) pass() {
	sc.t.Helper()
	sc.clock = sc.clock.Add(time.Minute)
	if err := sc.s.Pass(context.Background()); err != nil {
		sc.t.Fatalf("pass: %v\n%s", err, sc.log)
	}
}

func (sc *scenario) wantStatus(sha, state, desc string) {
	sc.t.Helper()
	got, _ := sc.forge.last()
	if got == nil || got["sha"] != sha || got["state"] != state || got["context"] != StatusContext || !strings.Contains(got["description"], desc) {
		sc.t.Fatalf("last status = %v, want %s %s %q on %s", got, StatusContext, state, desc, short(sha))
	}
}

func (sc *scenario) wantActive(want string) {
	sc.t.Helper()
	b, err := os.ReadFile(filepath.Join(sc.s.Dir, ActiveFile))
	if err != nil {
		sc.t.Fatal(err)
	}
	if string(b) != want {
		sc.t.Fatalf("active = %q, want %q", b, want)
	}
}

const staging = "The full vm chain could not run: the machine's egress refuses Let's Encrypt staging " +
	"(acme-staging-v02.api.letsencrypt.org). Criterion: the vm chain issues a staging certificate."

func stepTable(result, commit string) string {
	return ResultMarker + "\n## Live proof of ONS-41\nResult: " + result + "\nCommit: " + commit + "\n" +
		"Criterion: the vm chain issues a staging certificate.\n\n| Step | Command | Result |\n| --- | --- | --- |\n" +
		"| sandbox | `sandbox create --wait` | passed |\n| seed-vm | `make vm-chain STEP=seed` | passed |\n"
}

// Scenario: A vm chain the story's machine cannot run. The supervisor
// requests a live proof; the kit holds the merge with a pending live-proof
// status and starts the job on the story's branch with the criterion; the
// job's step table, commented on the pull request, fails the gate, then,
// after a fix pushed as a new commit and a new request, passes it.
func TestAVMChainTheStorysMachineCannotRun(t *testing.T) {
	sc := newScenario(t)
	sc.pass()
	if _, n := sc.forge.last(); n != 0 {
		t.Fatalf("a pass with no request set %d statuses", n)
	}
	sc.wantActive("")

	// The request, while the story's review still runs: Sortie refuses the
	// fix run, and the merge is held already.
	sc.sortie.request(5, staging, sc.clock)
	sc.sortie.refuse = "running"
	sc.pass()
	sc.wantStatus(headA, "pending", "the live-proof job waits to start")
	sc.wantActive("")
	if len(sc.sortie.fixes) != 0 {
		t.Fatalf("fixes = %v, want none yet", sc.sortie.fixes)
	}

	// The next pass starts the job: a fix run of ONS-41 whose message opens
	// with the marker and holds the branch, the pull request and the criterion.
	sc.pass()
	if len(sc.sortie.fixes) != 1 {
		t.Fatalf("fixes = %v, want one", sc.sortie.fixes)
	}
	fix := sc.sortie.fixes[0]
	if fix["identifier"] != "ONS-41" || !strings.HasPrefix(fix["message"], RequestMarker+"\n") {
		t.Fatalf("fix = %v, want a live-proof fix run of ONS-41", fix)
	}
	for _, want := range []string{"Pull request: " + homelabPR, "Branch: sortie/ONS-41", "action 5", staging} {
		if !strings.Contains(fix["message"], want) {
			t.Errorf("the job's message lacks %q:\n%s", want, fix["message"])
		}
	}
	if _, named := fix["profile"]; named || strings.Contains(fix["message"], "Profile:") {
		t.Errorf("a request that names no profile started a job with one: %v", fix)
	}
	sc.wantStatus(headA, "pending", "the live-proof job runs")
	sc.wantActive("ONS-41\n")
	_, n := sc.forge.last()
	sc.pass()
	if _, again := sc.forge.last(); again != n {
		t.Errorf("an unchanged status was set again (%d, then %d)", n, again)
	}
	if len(sc.sortie.fixes) != 1 {
		t.Errorf("the job was started again: %v", sc.sortie.fixes)
	}

	// Someone else's comment proves nothing.
	sc.forge.comment("someone", stepTable("proven", headA), sc.clock)
	sc.pass()
	sc.wantStatus(headA, "pending", "the live-proof job runs")

	// The job's step table: unproven. The gate fails, and the job's egress ends.
	sc.forge.comment(kitLogin, stepTable("unproven", headA), sc.clock)
	sc.pass()
	sc.wantStatus(headA, "failure", "unproven at aaaaaaa")
	if got, _ := sc.forge.last(); !strings.Contains(got["target_url"], "issuecomment-") {
		t.Errorf("the failure does not link the comment: %v", got)
	}
	sc.wantActive("")

	// A new request starts a second job; it pushes a fix as a new commit
	// (Sortie pushes it after the run) and proves the chain at it.
	sc.sortie.request(9, staging+" The seed step failed on a missing package; fixed on the branch.", sc.clock)
	sc.pass()
	if len(sc.sortie.fixes) != 2 || !strings.Contains(sc.sortie.fixes[1]["message"], "action 9") {
		t.Fatalf("fixes = %v, want a second job for action 9", sc.sortie.fixes)
	}
	sc.wantStatus(headA, "pending", "the live-proof job runs")
	sc.forge.set(headB, "open")
	sc.pass()
	sc.wantStatus(headB, "pending", "the live-proof job runs")
	sc.forge.comment(kitLogin, stepTable("proven", headB[:12]), sc.clock)
	sc.pass()
	sc.wantStatus(headB, "success", "proven at bbbbbbb")
	sc.wantActive("")

	// A commit after the proof is not proven.
	sc.forge.set(headC, "open")
	sc.pass()
	sc.wantStatus(headC, "pending", "the head moved on")

	// The pull request closes: the proof ends.
	sc.forge.set(headC, "closed")
	sc.pass()
	rec, err := sc.s.load()
	if err != nil || len(rec.Proofs) != 0 || rec.LastAction != 9 {
		t.Fatalf("record = %+v, %v; want no proof and action 9 read", rec, err)
	}
	// An action already read opens nothing again.
	sc.pass()
	if rec, _ := sc.s.load(); len(rec.Proofs) != 0 {
		t.Errorf("a read action opened a proof again: %+v", rec.Proofs)
	}
}

// A live proof the supervisor ran by hand before the job started (design
// D9) settles it: the kit starts no job.
func TestAResultBeforeTheJobStartsSettlesIt(t *testing.T) {
	sc := newScenario(t)
	sc.sortie.request(3, staging, sc.clock)
	sc.forge.comment(kitLogin, stepTable("proven", headA), sc.clock.Add(time.Second))
	sc.sortie.refuse = "running"
	sc.pass()
	sc.pass()
	if len(sc.sortie.fixes) != 0 {
		t.Errorf("fixes = %v, want none", sc.sortie.fixes)
	}
	sc.wantStatus(headA, "success", "proven at aaaaaaa")
}

// A comment from before the request is an older proof's.
func TestAnOlderResultDoesNotCount(t *testing.T) {
	sc := newScenario(t)
	sc.forge.comment(kitLogin, stepTable("proven", headA), sc.clock.Add(-time.Hour))
	sc.sortie.request(3, staging, sc.clock)
	sc.pass()
	sc.wantStatus(headA, "pending", "the live-proof job runs")
}

// A job that never comments loses the live-proof egress after maxRun, and
// the status says to request it again.
func TestAJobWithoutAResult(t *testing.T) {
	sc := newScenario(t)
	sc.sortie.request(3, staging, sc.clock)
	sc.pass()
	sc.wantActive("ONS-41\n")
	sc.clock = sc.clock.Add(maxRun)
	sc.pass()
	sc.wantActive("")
	sc.wantStatus(headA, "pending", "ended without a result")
}

func TestParseResult(t *testing.T) {
	for name, tc := range map[string]struct {
		body string
		want *Result
	}{
		"proven":           {stepTable("proven", headA), &Result{Proven: true, Commit: headA}},
		"unproven, CRLF":   {strings.ReplaceAll(stepTable("unproven", "abc1234"), "\n", "\r\n"), &Result{Commit: "abc1234"}},
		"no marker":        {strings.TrimPrefix(stepTable("proven", headA), ResultMarker), nil},
		"no commit":        {ResultMarker + "\nResult: proven\n", nil},
		"a short commit":   {ResultMarker + "\nResult: proven\nCommit: abc12\n", nil},
		"another word":     {ResultMarker + "\nResult: maybe\nCommit: abc1234\n", nil},
		"the request mark": {RequestMarker + "\nResult: proven\nCommit: abc1234\n", nil},
	} {
		got := parseResult(tc.body)
		if (got == nil) != (tc.want == nil) || (got != nil && (got.Proven != tc.want.Proven || got.Commit != tc.want.Commit)) {
			t.Errorf("%s: %+v, want %+v", name, got, tc.want)
		}
	}
}

// Scenario: A parked story's live criterion. Sortie itself starts the
// live-proof job of a parked story when the supervisor requests it, on
// the head of the story's pull request, and keeps the park. The kit asks
// for no fix run, which Sortie refuses for a parked story; it gives the
// job the live-proof egress and holds the merge until the job's result
// proves the criterion at the head.
func TestAParkedStorysLiveCriterion(t *testing.T) {
	sc := newScenario(t)
	sc.sortie.parked = []string{"ONS-41"}
	sc.sortie.request(7, staging, sc.clock)
	sc.pass()
	if len(sc.sortie.fixes) != 0 {
		t.Fatalf("fixes = %v, want none: Sortie runs the job of a parked story", sc.sortie.fixes)
	}
	sc.wantStatus(headA, "pending", "the live-proof job runs")
	sc.wantActive("ONS-41\n")

	sc.forge.comment(kitLogin, stepTable("proven", headA)+"sortie:live-proof proven "+headA+"\n", sc.clock)
	sc.pass()
	sc.wantStatus(headA, "success", "proven at "+short(headA))
	sc.wantActive("")
	if len(sc.sortie.fixes) != 0 {
		t.Fatalf("fixes = %v, want none", sc.sortie.fixes)
	}
}
