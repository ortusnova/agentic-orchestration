// stage0-live-proofs turns a supervisor's live-proof request into a
// live-proof job, and its result into the pull request's live-proof status
// (spec store agent-platform/run-supervision, "A live criterion a story's
// machine cannot run becomes a live-proof job").
//
// One pass (ortus-stage0-live-proofs.timer runs it every minute):
//
//   - Each new live_proof action of Sortie's supervisor (GET /api/v1/state,
//     supervisor_actions) opens a live proof of its pull request, with the
//     action's evidence as the criterion.
//   - The pull request's head gets the commit status live-proof, pending
//     until the job's result proves it. Sortie's auto_merge requires CI,
//     and CI is every status and check run of the head, so a pull request
//     whose live criterion is unproven is not merged.
//   - The job is a review fix run on the story's branch, which Sortie
//     starts on POST /api/v1/{identifier}/fix with a message that opens with
//     the live-proof marker; the workflow's fix prompt runs it as a live
//     proof. While it runs, its identifier is in the active file, and the
//     egress gate behind Pomerium gives its CONNECTs the live-proof egress.
//   - The job comments its result on the pull request. The newest such
//     comment by the kit's GitHub account sets the status: success when it
//     proved the criterion at the head, failure when it did not.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	// RequestMarker opens the message of a live-proof job's fix run; the
	// workflow's fix prompt keys on it.
	RequestMarker = "<!-- live-proof -->"
	// ResultMarker opens the job's comment with its result.
	ResultMarker = "<!-- live-proof-result -->"
	// StatusContext is the commit status the kit sets on the head.
	StatusContext = "live-proof"

	actionLiveProof = "live_proof"

	// maxRun bounds how long a started job keeps the live-proof egress
	// without a result: past it the job is taken as ended.
	maxRun = 12 * time.Hour
)

// Syncer runs one pass against Sortie and the forge.
type Syncer struct {
	Sortie      string
	SortieToken string
	GitHub      string
	GitHubToken string
	// Dir holds record.json and active.
	Dir    string
	Client *http.Client
	Log    *slog.Logger
	Now    func() time.Time
}

// Record is what the kit keeps between passes, in Dir/record.json.
type Record struct {
	// LastAction is the newest supervisor action a pass has read.
	LastAction int64 `json:"last_action"`
	// Proofs holds each open live proof by its pull request,
	// owner/repo#number.
	Proofs map[string]*Proof `json:"proofs"`
}

// Proof is the live proof of one pull request's criterion.
type Proof struct {
	Identifier  string     `json:"identifier"`
	PullRequest string     `json:"pull_request"`
	Criterion   string     `json:"criterion"`
	Action      int64      `json:"action"`
	RequestedAt time.Time  `json:"requested_at"`
	Started     *time.Time `json:"started,omitempty"`
	Result      *Result    `json:"result,omitempty"`
	// Posted is the last status the pass set: head, state and description.
	Posted string `json:"posted,omitempty"`
}

// Result is a live-proof job's result, from its comment.
type Result struct {
	Proven bool      `json:"proven"`
	Commit string    `json:"commit"`
	URL    string    `json:"url"`
	At     time.Time `json:"at"`
}

// ActiveFile is the file of Dir the egress gate reads (cp-egress
// -live-proof-jobs-file).
const ActiveFile = "active"

func (s *Syncer) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Pass runs one pass. A pull request it cannot read now is tried again on
// the next pass.
func (s *Syncer) Pass(ctx context.Context) error {
	rec, err := s.load()
	if err != nil {
		return err
	}
	me, err := s.login(ctx)
	if err != nil {
		return fmt.Errorf("the forge: %w", err)
	}
	actions, err := s.actions(ctx)
	if err != nil {
		return fmt.Errorf("the state of sortie: %w", err)
	}
	for _, a := range actions {
		if a.ID <= rec.LastAction {
			continue
		}
		rec.LastAction = a.ID
		if a.Action != actionLiveProof || a.Result != "done" {
			continue
		}
		if a.PullRequest == nil || a.IssueIdentifier == nil {
			s.Log.Warn("live proof requested of a story with no pull request", slog.Int64("action", a.ID))
			continue
		}
		rec.Proofs[*a.PullRequest] = &Proof{
			Identifier: *a.IssueIdentifier, PullRequest: *a.PullRequest,
			Criterion: a.Evidence, Action: a.ID, RequestedAt: a.At.UTC(),
		}
		s.Log.Info("live proof requested", slog.String("issue", *a.IssueIdentifier), slog.String("pull_request", *a.PullRequest), slog.Int64("action", a.ID))
	}
	keys := make([]string, 0, len(rec.Proofs))
	for k := range rec.Proofs {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var errs []error
	for _, k := range keys {
		closed, err := s.follow(ctx, rec.Proofs[k], me)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", k, err))
			continue
		}
		if closed {
			delete(rec.Proofs, k)
		}
	}
	if err := s.save(rec); err != nil {
		return err
	}
	return errors.Join(errs...)
}

// follow moves one live proof on: its result, its job, and its status. It
// reports whether the pull request is closed, which ends the proof.
func (s *Syncer) follow(ctx context.Context, p *Proof, me string) (bool, error) {
	ref, err := parsePullRequest(p.PullRequest)
	if err != nil {
		return false, err
	}
	pr, err := s.pull(ctx, ref)
	if err != nil {
		return false, err
	}
	if pr.State != "open" {
		s.Log.Info("live proof ends with its pull request", slog.String("issue", p.Identifier), slog.String("pull_request", p.PullRequest), slog.Bool("merged", pr.Merged))
		return true, nil
	}
	result, err := s.result(ctx, ref, me, p.RequestedAt)
	if err != nil {
		return false, err
	}
	if result != nil && (p.Result == nil || !result.At.Equal(p.Result.At)) {
		p.Result = result
		s.Log.Info("live proof result", slog.String("issue", p.Identifier), slog.String("pull_request", p.PullRequest), slog.Bool("proven", result.Proven), slog.String("commit", result.Commit))
	}
	if p.Started == nil && p.Result == nil {
		if err := s.start(ctx, p, pr); err != nil {
			s.Log.Warn("live-proof job not started yet", slog.String("issue", p.Identifier), slog.String("error", err.Error()))
		}
	}
	state, desc, target := p.status(pr.Head.SHA, s.now())
	posted := pr.Head.SHA + " " + state + " " + desc
	if posted != p.Posted {
		if err := s.setStatus(ctx, ref, pr.Head.SHA, state, desc, target); err != nil {
			return false, err
		}
		p.Posted = posted
	}
	return false, nil
}

// status is the live-proof status of head.
func (p *Proof) status(head string, now time.Time) (state, desc, target string) {
	switch r := p.Result; {
	case r != nil && r.Proven && sameCommit(r.Commit, head):
		return "success", "proven at " + short(r.Commit), r.URL
	case r != nil && r.Proven:
		return "pending", "proven at " + short(r.Commit) + "; the head moved on: the supervisor requests a new live proof", r.URL
	case r != nil:
		return "failure", "unproven at " + short(r.Commit) + ": see the live-proof comment", r.URL
	case p.Started == nil:
		return "pending", "the live-proof job waits to start", ""
	case now.Sub(*p.Started) > maxRun:
		return "pending", "the live-proof job ended without a result: the supervisor requests it again", ""
	}
	return "pending", "the live-proof job runs", ""
}

// running reports whether the proof's job runs: started, with no result
// yet, and not past maxRun.
func (p *Proof) running(now time.Time) bool {
	return p.Started != nil && p.Result == nil && now.Sub(*p.Started) <= maxRun
}

func sameCommit(commit, head string) bool {
	return len(commit) >= 7 && strings.HasPrefix(head, commit)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// start asks Sortie for the live-proof job: a review fix run on the
// story's branch whose message carries the marker and the criterion.
func (s *Syncer) start(ctx context.Context, p *Proof, pr pull) error {
	msg := RequestMarker + "\n" + jobMessage(p, pr.Head.Ref)
	var answer struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	status, err := s.call(ctx, http.MethodPost, s.Sortie+"/api/v1/"+url.PathEscape(p.Identifier)+"/fix", s.SortieToken,
		map[string]string{"message": msg}, &answer)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("sortie answered %d %s: %s", status, answer.Error.Code, answer.Error.Message)
	}
	now := s.now()
	p.Started = &now
	s.Log.Info("live-proof job started", slog.String("issue", p.Identifier), slog.String("pull_request", p.PullRequest), slog.String("branch", pr.Head.Ref))
	return nil
}

// jobMessage is what the job reads after the marker: where it runs from,
// where it comments, and the criterion.
func jobMessage(p *Proof, branch string) string {
	return "Live proof of " + p.Identifier + ", requested by the supervisor (action " + strconv.FormatInt(p.Action, 10) + ").\n" +
		"Pull request: " + p.PullRequest + "\n" +
		"Branch: " + branch + "\n" +
		"Criterion, and why the story's machine could not run it, as the supervisor wrote them:\n" + p.Criterion
}

var resultLine = regexp.MustCompile(`(?m)^Result:\s*(proven|unproven)\s*$`)
var commitLine = regexp.MustCompile(`(?m)^Commit:\s*([0-9a-f]{7,40})\s*$`)

// parseResult reads a result comment's body: nil when it is not one.
func parseResult(body string) *Result {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	if !strings.HasPrefix(strings.TrimSpace(body), ResultMarker) {
		return nil
	}
	r, c := resultLine.FindStringSubmatch(body), commitLine.FindStringSubmatch(body)
	if r == nil || c == nil {
		return nil
	}
	return &Result{Proven: r[1] == "proven", Commit: c[1]}
}

type pullRef struct {
	owner, repo string
	number      int
}

func parsePullRequest(s string) (pullRef, error) {
	project, num, ok := strings.Cut(s, "#")
	owner, repo, ok2 := strings.Cut(project, "/")
	n, err := strconv.Atoi(num)
	if !ok || !ok2 || err != nil || owner == "" || repo == "" || n < 1 {
		return pullRef{}, fmt.Errorf("pull request %q: want owner/repo#number", s)
	}
	return pullRef{owner, repo, n}, nil
}

func (r pullRef) path() string {
	return "/repos/" + url.PathEscape(r.owner) + "/" + url.PathEscape(r.repo)
}

type pull struct {
	State  string `json:"state"`
	Merged bool   `json:"merged"`
	Head   struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
}

func (s *Syncer) pull(ctx context.Context, r pullRef) (pull, error) {
	var pr pull
	if err := s.github(ctx, http.MethodGet, r.path()+"/pulls/"+strconv.Itoa(r.number), nil, &pr); err != nil {
		return pull{}, err
	}
	if pr.Head.SHA == "" {
		return pull{}, errors.New("the pull request has no head")
	}
	return pr, nil
}

type comment struct {
	Body    string    `json:"body"`
	HTMLURL string    `json:"html_url"`
	Created time.Time `json:"created_at"`
	User    struct {
		Login string `json:"login"`
	} `json:"user"`
}

// result returns the newest result comment by me on the pull request,
// written since the request; nil when there is none.
func (s *Syncer) result(ctx context.Context, r pullRef, me string, since time.Time) (*Result, error) {
	var newest *Result
	for page := 1; page <= 10; page++ {
		var cs []comment
		q := url.Values{"since": {since.Format(time.RFC3339)}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := s.github(ctx, http.MethodGet, r.path()+"/issues/"+strconv.Itoa(r.number)+"/comments?"+q.Encode(), nil, &cs); err != nil {
			return nil, err
		}
		for _, c := range cs {
			if c.User.Login != me || c.Created.Before(since) {
				continue
			}
			res := parseResult(c.Body)
			if res == nil || (newest != nil && !c.Created.After(newest.At)) {
				continue
			}
			res.URL, res.At = c.HTMLURL, c.Created.UTC()
			newest = res
		}
		if len(cs) < 100 {
			break
		}
	}
	return newest, nil
}

func (s *Syncer) setStatus(ctx context.Context, r pullRef, sha, state, desc, target string) error {
	body := map[string]string{"state": state, "description": desc, "context": StatusContext}
	if target != "" {
		body["target_url"] = target
	}
	if err := s.github(ctx, http.MethodPost, r.path()+"/statuses/"+url.PathEscape(sha), body, nil); err != nil {
		return fmt.Errorf("status %s on %s: %w", state, short(sha), err)
	}
	return nil
}

func (s *Syncer) login(ctx context.Context) (string, error) {
	var u struct {
		Login string `json:"login"`
	}
	if err := s.github(ctx, http.MethodGet, "/user", nil, &u); err != nil {
		return "", err
	}
	if u.Login == "" {
		return "", errors.New("the token names no account")
	}
	return u.Login, nil
}

type action struct {
	ID              int64     `json:"id"`
	Action          string    `json:"action"`
	IssueIdentifier *string   `json:"issue_identifier"`
	Evidence        string    `json:"evidence"`
	Result          string    `json:"result"`
	PullRequest     *string   `json:"pull_request"`
	At              time.Time `json:"at"`
}

// actions returns the supervisor actions Sortie serves, oldest first.
func (s *Syncer) actions(ctx context.Context) ([]action, error) {
	var st struct {
		SupervisorActions []action `json:"supervisor_actions"`
	}
	status, err := s.call(ctx, http.MethodGet, s.Sortie+"/api/v1/state", "", nil, &st)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("answered %d", status)
	}
	slices.SortFunc(st.SupervisorActions, func(a, b action) int { return int(a.ID - b.ID) })
	return st.SupervisorActions, nil
}

func (s *Syncer) github(ctx context.Context, method, path string, body, out any) error {
	status, err := s.call(ctx, method, s.GitHub+path, s.GitHubToken, body, out)
	if err != nil {
		return err
	}
	if status < 200 || status > 299 {
		return fmt.Errorf("%s %s answered %d", method, strings.SplitN(path, "?", 2)[0], status)
	}
	return nil
}

// call sends one JSON request and decodes a JSON answer into out.
func (s *Syncer) call(ctx context.Context, method, u, token string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return 0, err
	}
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil && resp.StatusCode/100 == 2 {
			return resp.StatusCode, fmt.Errorf("the answer: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (s *Syncer) load() (*Record, error) {
	rec := &Record{Proofs: map[string]*Proof{}}
	b, err := os.ReadFile(filepath.Join(s.Dir, "record.json"))
	if errors.Is(err, os.ErrNotExist) {
		return rec, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, rec); err != nil {
		return nil, fmt.Errorf("record.json: %w", err)
	}
	if rec.Proofs == nil {
		rec.Proofs = map[string]*Proof{}
	}
	return rec, nil
}

// save writes the record and the active file, each in one rename.
func (s *Syncer) save(rec *Record) error {
	if err := os.MkdirAll(s.Dir, 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(s.Dir, "record.json"), append(b, '\n')); err != nil {
		return err
	}
	var active []string
	now := s.now()
	for _, p := range rec.Proofs {
		if p.running(now) {
			active = append(active, p.Identifier)
		}
	}
	slices.Sort(active)
	text := strings.Join(active, "\n")
	if text != "" {
		text += "\n"
	}
	return writeFile(filepath.Join(s.Dir, ActiveFile), []byte(text))
}

func writeFile(name string, b []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil { // #nosec G703 -- the operator names the directory
		return err
	}
	return os.Rename(tmp, name) // #nosec G703 -- the operator names the directory
}
