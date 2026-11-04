// The stage-0 kit's live proofs (agent-platform/run-supervision#A live
// criterion a story's machine cannot run becomes a live-proof job), checked
// against Sortie itself as stage0_review_test.go checks the review gate.
// stage0-live-proofs (dev/stage0/live-proofs) turns the supervisor's
// live-proof request into Sortie's fix run with the live-proof marker, and
// the job's result comment into the head's live-proof status; here Sortie's
// supervisor action, prompt code, worker matcher, GitHub adapter and
// auto-merge run that job and that gate with the kit's workflow, against a
// fake worker and a fake forge.

package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/persistence"
	"github.com/sortie-ai/sortie/internal/scm/github"
	"github.com/sortie-ai/sortie/internal/worker"
)

const (
	// liveProofMarker and liveProofResultMarker are stage0-live-proofs'
	// RequestMarker and ResultMarker (dev/stage0/live-proofs/proofs.go).
	liveProofMarker       = "<!-- live-proof -->"
	liveProofResultMarker = "<!-- live-proof-result -->"

	liveProofCriterion = "The full vm chain could not run: the machine's egress refuses Let's Encrypt staging " +
		"(acme-staging-v02.api.letsencrypt.org). Criterion: the vm chain issues a staging certificate."
)

// liveProofMessage is the fix run's message stage0-live-proofs sends for
// the supervisor's action 5 on homelab#41 (jobMessage in proofs.go).
func liveProofMessage(identifier string) string {
	return liveProofMarker + "\nLive proof of " + identifier + ", requested by the supervisor (action 5).\n" +
		"Pull request: ortusnova/homelab#41\nBranch: sortie/" + identifier + "\n" +
		"Criterion, and why the story's machine could not run it, as the supervisor wrote them:\n" + liveProofCriterion
}

// The live-proof job's prompt: what the kit's fix prompt says to a fix
// run whose message carries the live-proof marker.
var liveProofPromptRules = []string{
	"The supervisor asks for a live proof.",
	"this run is that live-proof job",
	"a homelab job runs on taichi, with nested virtualization",
	"Let's Encrypt staging",
	"Read the repository's `AGENTS.md` and follow it.",
	"`sandbox create --wait`",
	"the cheapest proof that runs the changed code",
	"open an issue on the repository for every failure whose cause you do not know",
	"fix it with new commits on top of the branch",
	"comment the result on the pull request the request names",
	"`Authorization: Bearer $SORTIE_GITHUB_TOKEN`",
	"first line is `" + liveProofResultMarker + "`",
	"`Result: proven` or `Result: unproven`",
	"`Commit: ` with the full sha of `git rev-parse HEAD`",
	"with `Result: proven`, a line `sortie:live-proof proven ` followed by that same sha",
	"Sortie's merge reads the `sortie:live-proof proven` line",
	"the step table, one row per step you ran, `| Step | Command | Result |`",
	"sets the pull request's `live-proof` status, which the merge requires",
	"lets the supervisor merge it once CI passes",
	"Never rewrite a commit that is already pushed",
	"Do not push",
}

// Scenario: A vm chain the story's machine cannot run. The supervisor's
// request, as the kit sends it to Sortie's fix route, schedules a review
// fix run on the story's pull request whose prompt is the live-proof job's:
// the story's branch, its pull request and the criterion, and homelab's
// AGENTS.md rules.
func TestStage0_ALiveProofRequestStartsTheLiveProofJob(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	h := newSupervisorHarness(t, stateWithReviewReaction(t, "id-41", 41))
	got := h.o.Supervisor().FixRun(t.Context(), "id-41-ident", liveProofMessage("ONS-41"))
	if got.Status != SupervisorDone {
		t.Fatalf("the live-proof job's fix run = %+v, want done", got)
	}
	var retry *RetryEntry
	onLoop(t, h.o, func() { retry = h.o.state.RetryAttempts["id-41"] })
	if retry == nil || retry.ReactionKind != ReactionKindReview {
		t.Fatalf("scheduled run = %+v, want a review fix run", retry)
	}
	text := renderFix(t, w, retry.ContinuationContext)
	wantPrompt(t, w.name, text, liveProofPromptRules...)
	wantPrompt(t, w.name, text, "You are fixing the pull request of story **ONS-41**",
		`Pull request: ortusnova/homelab#41\nBranch: sortie/ONS-41`,
		`egress refuses Let's Encrypt staging (acme-staging-v02.api.letsencrypt.org). Criterion: the vm chain issues a staging certificate.`)
	for _, never := range []string{"The operator looked at the pull request", "You are reviewing", "CI fails on", "Rebase "} {
		if strings.Contains(text, never) {
			t.Errorf("the live-proof job's prompt holds %q", never)
		}
	}

	// A supervisor's or operator's message without the marker is an
	// ordinary review fix, not a live proof.
	plain := renderFix(t, w, map[string]any{reviewCommentsKey: buildReviewTemplateMap([]domain.ReviewComment{{
		ID: "supervisor-1", Reviewer: "supervisor", Body: "The worker column is missing.",
	}})})
	wantPrompt(t, w.name, plain, "The operator looked at the pull request")
	if strings.Contains(plain, "The supervisor asks for a live proof.") {
		t.Error("a fix message without the marker got the live-proof prompt")
	}
}

// The live-proof job of a homelab story runs where homelab's profile
// allows it: Sortie's matcher, with the kit's workflow, offers it to the
// fake worker taichi, which declares nested virtualization, and never to
// p1, which does not. The job holds the forge's API credential only as a
// placeholder, for its result comment; nono on p1 holds the token.
func TestStage0_AHomelabLiveProofRunsOnTaichi(t *testing.T) {
	cfg := loadStage0(t, "WORKFLOW.md")
	repos := cfg.Environment.Remote.Repositories
	i := slices.IndexFunc(repos, func(r config.Repository) bool { return r.Name == "homelab" })
	if i < 0 {
		t.Fatal("the workflow lists no homelab repository")
	}
	cfg.Environment.SetRepository(&repos[i])
	job := remoteJob(context.Background(), cfg, domain.Issue{ID: "h41", Identifier: "ONS-41", Title: "Issue a staging certificate"}, nil, "agent-client-protocol", "agent-acp")

	m := worker.NewMatcher(worker.NewTTLLeaser(time.Minute), time.Now, nil)
	free := worker.Free{CPU: 32, MemGiB: 128, DiskGiB: 500}
	m.CheckIn("p1", worker.Declaration{OS: "linux", Arch: "amd64", KVM: true, Backends: []string{"smolvm"}, RunSlots: 3, Free: free})
	m.CheckIn("taichi", worker.Declaration{OS: "linux", Arch: "amd64", KVM: true, Nested: true, Backends: []string{"smolvm"}, RunSlots: 2, Free: free})
	m.Submit(job)
	if offer, ok, err := m.Poll("p1"); ok || err != nil {
		t.Fatalf("p1 was offered the homelab live proof: %+v, %v", offer.Job, err)
	}
	offer, ok, err := m.Poll("taichi")
	if !ok || err != nil {
		t.Fatalf("taichi was not offered the homelab live proof: %v", err)
	}
	if offer.Run.Image != "homelab.smolmachine" {
		t.Errorf("image = %q, want the homelab pack", offer.Run.Image)
	}
	if got := offer.Run.Env["SORTIE_GITHUB_TOKEN"]; got != credentialPlaceholder("github_api") {
		t.Errorf("SORTIE_GITHUB_TOKEN = %q, want the github_api placeholder", got)
	}
	var cred *worker.RunCredential
	for i := range offer.Run.Credentials {
		if offer.Run.Credentials[i].Name == "github_api" {
			cred = &offer.Run.Credentials[i]
		}
	}
	if cred == nil || !slices.Equal(cred.Hosts, []string{"api.github.com"}) {
		t.Errorf("credentials = %+v, want github_api for api.github.com only", offer.Run.Credentials)
	}
}

// liveProofForge is GitHub for one pull request: its head, the kit's
// live-proof status on it, and a passing check run.
type liveProofForge struct {
	*httptest.Server
	mu     sync.Mutex
	status string // the live-proof status's state; empty: none
}

func newLiveProofForge(t *testing.T) *liveProofForge {
	f := &liveProofForge{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch p := r.URL.Path; {
		case strings.HasSuffix(p, "/pulls/41"):
			_ = json.NewEncoder(w).Encode(map[string]any{"number": 41, "state": "open", "head": map[string]string{"sha": "deadbeef", "ref": "sortie/ONS-41"}})
		case strings.HasSuffix(p, "/commits/deadbeef/status"):
			statuses := []map[string]string{}
			if f.status != "" {
				statuses = append(statuses, map[string]string{"state": f.status, "context": "live-proof"})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"state": f.status, "statuses": statuses})
		case strings.HasSuffix(p, "/commits/deadbeef/check-runs"):
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "check_runs": []map[string]any{
				{"name": "test", "status": "completed", "conclusion": "success"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

// Scenario: A vm chain the story's machine cannot run, the gate. The kit's
// live-proof status on the head is CI to Sortie's GitHub adapter: while
// the live proof is pending, and once its result says unproven, Sortie does
// not merge the pull request, though its review and every check passed;
// once the result proves the criterion at the head, Sortie merges it.
func TestStage0_AnUnprovenLiveCriterionIsNotMerged(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	forge := newLiveProofForge(t)
	adapter, err := github.NewGitHubSCMAdapter(map[string]any{"api_key": "kit-token", "endpoint": forge.URL})
	if err != nil {
		t.Fatal(err)
	}
	ci := adapter.(interface {
		GetCIStatus(context.Context, int, string, string) (string, error)
	})
	for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
		t.Run(value, func(t *testing.T) {
			s := newFixScenario(t, w, value)
			s.exitRun("")
			for _, tc := range []struct {
				status, ci string
				merge      bool
			}{
				{"pending", "pending", false},
				{"failure", "failing", false},
				{"success", "success", true},
			} {
				forge.mu.Lock()
				forge.status = tc.status
				forge.mu.Unlock()
				got, err := ci.GetCIStatus(context.Background(), 41, "ortusnova", "homelab")
				if err != nil {
					t.Fatal(err)
				}
				if got != tc.ci {
					t.Errorf("live proof %s: Sortie reads CI as %q, want %s", tc.status, got, tc.ci)
				}
				if merged := s.autoMerge(got); merged != tc.merge {
					t.Errorf("live proof %s (CI %s): merged = %v, want %v", tc.status, got, merged, tc.merge)
				}
			}
		})
	}
}

// liveProofComments is the forge's conversation comments on the pull
// request.
type liveProofComments []domain.ReviewComment

func (c liveProofComments) FetchPullRequestComments(context.Context, int, string, string) ([]domain.ReviewComment, error) {
	return c, nil
}

// liveProofResult is the result comment the live-proof prompt asks the
// job to write, proven or not, at commit.
func liveProofResult(proven bool, commit string) string {
	body := liveProofResultMarker + "\nResult: unproven\nCommit: " + commit + "\nCriterion: the vm chain issues a staging certificate.\n"
	if proven {
		body = liveProofResultMarker + "\nResult: proven\nCommit: " + commit + "\nCriterion: the vm chain issues a staging certificate.\n" +
			"sortie:live-proof proven " + commit + "\n"
	}
	return body + "\n| Step | Command | Result |\n| --- | --- | --- |\n| seed-usb | sandbox chain seed-usb | DONE |\n"
}

// Scenario: A vm chain the story's machine cannot run, the supervisor's
// merge. A story whose review left a criterion to a live proof is not
// merged by the supervisor, though CI passed, until the live-proof job's
// result comment, as the kit's prompt has it write one, proves the
// criterion at the pull request's head.
func TestStage0_TheSupervisorMergesALiveCriterionOnlyOnceProven(t *testing.T) {
	const head = "9fad581d0e4b2c7a1f3e5d6c8b9a0f1e2d3c4b5a"
	for _, tc := range []struct {
		name     string
		comments liveProofComments
		merged   bool
	}{
		{"no result", nil, false},
		{"unproven at the head", liveProofComments{{ID: "c1", Reviewer: "ortus-kit", Body: liveProofResult(false, head)}}, false},
		{"proven at an earlier commit", liveProofComments{{ID: "c1", Reviewer: "ortus-kit", Body: liveProofResult(true, "23cc973a1b2c3d4e5f60718293a4b5c6d7e8f901")}}, false},
		{"proven at the head", liveProofComments{{ID: "c1", Reviewer: "ortus-kit", Body: liveProofResult(true, head)}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newSupervisorHarness(t, stateWithReviewReaction(t, "id-41", 41))
			h.tracker.labels = []string{config.ReviewCriteriaLabel}
			h.forge.status.HeadSHA = head
			h.o.prCommentReader = tc.comments
			if err := h.store.UpsertCriteriaVerdict(t.Context(), persistence.CriteriaVerdict{
				IssueID: "id-41", Outcome: CriteriaOutcomeLiveProof, RecordedAt: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			got := h.o.Supervisor().Merge(t.Context(), "id-41-ident")
			if merged := got.Status == SupervisorDone && len(h.forge.merges) == 1; merged != tc.merged {
				t.Errorf("merge = %+v, merged %v; want merged %v", got, merged, tc.merged)
			}
			if !tc.merged && got.Code != "live_proof_unproven" {
				t.Errorf("refusal code %q, want live_proof_unproven", got.Code)
			}
		})
	}
}

// The review settles a live criterion by naming it for a live proof, not by
// reasoning about it and not by stopping blocked.
func TestStage0_TheReviewNamesALiveCriterion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("STAGE0_KIT"), "WORKFLOW.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	start := strings.Index(body, `{{- else if eq .stage.current "review" -}}`)
	end := strings.Index(body, "{{- else -}}")
	prompt := strings.Join(strings.Fields(body[start:end]), " ")
	for _, want := range []string{
		"A criterion that needs a live run this machine cannot do",
		"is settled by a live proof, not by reasoning about it",
		"`live proof needed:`",
		"record its verdict `live-proof`",
		"Do not stop `blocked` for it",
		"Sortie does not merge the pull request; the supervisor requests a live-proof job",
		"merges the pull request only once that job's result comment proves the criterion at its head",
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("review prompt lacks %q", want)
		}
	}
}

// The triage passes over a live-proof job's result comment, which the kit
// reads, and over a click-proofs record, which Sortie's merge reads: it
// starts no fix run. It lets the kit's preview comment through, which starts
// the review on the preview.
func TestStage0_TheTriagePassesOverALiveProofResult(t *testing.T) {
	result := map[string]any{"id": "issuecomment-3", "reviewer": "brian", "body": liveProofResultMarker + "\nResult: proven\nCommit: deadbeef\n"}
	operator := map[string]any{"id": "issuecomment-4", "reviewer": "brian", "body": "Quote " + liveProofResultMarker + " in the docs."}
	preview := map[string]any{"id": "issuecomment-5", "reviewer": "brian", "body": previewComment(uiHeadSHA)}
	record := map[string]any{"id": "issuecomment-6", "reviewer": "brian", "body": ClickProofsMarker + " " + uiHeadSHA + "\n\n- The worker column: pass."}
	for _, tc := range []struct {
		name     string
		comments []map[string]any
		want     string
	}{
		{"the result", []map[string]any{result}, "handled"},
		{"an operator's comment that names the marker", []map[string]any{operator}, "dispatch-agent"},
		{"both", []map[string]any{result, operator}, "dispatch-agent"},
		{"the preview's comment", []map[string]any{preview}, "dispatch-agent"},
		{"the result and the preview's comment", []map[string]any{result, preview}, "dispatch-agent"},
		{"a click-proofs record", []map[string]any{record}, "handled"},
		{"a click-proofs record and an operator's comment", []map[string]any{record, operator}, "dispatch-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := runTriage(t, tc.comments); got != tc.want {
				t.Errorf("disposition = %q, want %s", got, tc.want)
			}
		})
	}
}

// runTriage runs the kit's review_comments triage on comments, as Sortie
// runs it on the machine, and returns its disposition.
func runTriage(t *testing.T, comments []map[string]any) string {
	t.Helper()
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node on PATH: the machines run the triage with the pack's node")
	}
	script := loadStage0(t, "WORKFLOW.md").Reactions["review_comments"].Triage.Script
	dir := t.TempDir()
	input, out := filepath.Join(dir, "input.json"), filepath.Join(dir, "result.json")
	doc, _ := json.Marshal(map[string]any{"schema_version": 1, "reaction_kind": "review", "subject": comments})
	if err := os.WriteFile(input, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SORTIE_REACTION_KIND=review", "SORTIE_REACTION_INPUT="+input, "SORTIE_REACTION_RESULT="+out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("triage: %v: %s", err, b)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Disposition string }
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("triage result %s: %v", raw, err)
	}
	return got.Disposition
}
