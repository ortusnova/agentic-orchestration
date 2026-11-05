// The stage-0 kit's ui stories (agent-platform/run-supervision#A UI is
// approved as a prototype, and the built story merges after its review),
// checked against Sortie itself as stage0_review_test.go checks the review
// gate: a sortie-review:ui story goes through the review; the kit's comment
// with its preview's address starts the review on the preview, whose prompt
// asks the reviewer to click every action the criteria name there and, when
// they pass, to record `sortie:click-proofs passed <head sha>` on the pull
// request; Sortie merges it once that record names the head, with no person
// involved, and the merge makes it Done. An operator's comment still starts
// a fix run with the kit's fix prompt.

package orchestrator

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/prompt"
)

// previewMarker opens the kit's comment with a preview's address
// (workbench/src/workbench/preview.py).
const previewMarker = "<!-- workbench-preview -->"

// previewComment is the kit's comment on a ui story's pull request for one
// deployed head, as preview.py writes it.
func previewComment(sha string) string {
	return previewMarker + "\nPreview of ONS-97 at `" + sha + "`: https://workbench-ons-97.example/\n\n" +
		"This is the branch's workbench at that commit, read-only toward the tracker, against live data. " +
		"This comment starts the review on the preview: it clicks every action the story's acceptance criteria name " +
		"there and, when they pass, comments on ortusnova/agentic-orchestration#80 a line that starts with " +
		"`" + ClickProofsMarker + "` and names this commit. Sortie merges the pull request once that record names its head."
}

// The prompt of the review on the preview: it asks the reviewer to click
// every action the criteria name on the story's preview and, when they
// pass, to record the click-proofs for the head on the pull request.
var clickProofsPrompt = []string{
	"You are reviewing story **ONS-97** on its preview: Show worker activity",
	"The worker column shows each worker.",
	"The operator approved its interface as a design prototype before it was written",
	"Sortie merges its pull request once a comment on the pull request records the click-proofs on its preview for the pull request's head commit; no person approves or merges it.",
	`<comment>"\u003c!-- workbench-preview --\u003e\nPreview of ONS-97 at`,
	"Read its `AGENTS.md` first",
	"check that `git rev-parse HEAD` is the commit the kit's comment names",
	"Open the preview's address, exactly as the kit's comment gives it, in a headless browser",
	"with this machine's proxy settings: launch it with `proxy: { server: process.env.HTTP_PROXY }`",
	"When the address does not answer, or answers an error, that is a platform fault: stop with `blocked` in `.sortie/status`, and give on the lines after it the address and the error you got.",
	"Never ask a person to redeploy the preview: the kit redeploys one that does not answer on its own.",
	"Click every action the story's acceptance criteria name, on the story's preview, one at a time",
	"what the approved prototype shows",
	"When every action passes, comment on the pull request the kit's comment names, through GitHub's API",
	"`POST https://api.github.com/repos/<owner>/<repo>/issues/<number>/comments`",
	"The comment's first line is exactly `sortie:click-proofs passed <head sha>`, with the full commit that `git rev-parse HEAD` prints",
	"That record is what Sortie waits for, and then it merges the pull request.",
	"Write no other comment on the pull request",
	"When you add a commit, write no click-proofs record",
	"When an action fails and you can fix it, fix it on the branch, with a test, and write no record",
	"stop with `blocked` in `.sortie/status`",
	"Never rewrite a commit that is already pushed",
	"Do not push",
}

// renderUI renders the given turn of a run of a ui story on the review
// rule: the review itself with no continuation, else a reaction's run.
func renderUI(t *testing.T, w stage0Fix, turn int, continuation map[string]any) string {
	t.Helper()
	issue := domain.Issue{
		ID: "u", Identifier: "ONS-97", Title: "Show worker activity",
		Description: "## Acceptance criteria\n\n- The worker column shows each worker.",
		Labels:      []string{config.ReviewUILabel, w.review.Stage},
	}
	text, err := prompt.BuildReactionTurnPrompt(w.tmpl, issue.ToTemplateMap(), 0, turn, w.cfg.Agent.MaxTurns, continuation,
		prompt.WithStage(prompt.StageContext{Current: w.review.Name}))
	if err != nil {
		t.Fatalf("%s: render the ui story's run: %v", w.name, err)
	}
	return strings.Join(strings.Fields(text), " ")
}

// reviewCommentsOf is the review_comments continuation Sortie hands a run.
func reviewCommentsOf(comments ...domain.ReviewComment) map[string]any {
	return map[string]any{"review_comments": buildReviewTemplateMap(comments)}
}

// Scenario: A built UI story. It is built by a rule that is not the review,
// and its chain ends in the review. After a passing review with clean,
// green CI, Sortie waits for the click-proofs on its preview, asking no one
// to approve or merge it, and merges it once a record names its head.
func TestStage0_AUIStoryMergesOnceItsClickProofsAreRecorded(t *testing.T) {
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		for _, issue := range []domain.Issue{
			{ID: "u", Identifier: "ONS-97", Title: "Show worker activity", Labels: []string{config.ReviewUILabel}},
			{ID: "uc", Identifier: "ONS-98", Title: "Show worker activity", Labels: []string{config.ReviewUILabel, "claude-code"}},
		} {
			built := ResolveRule(issue, cfg.Dispatch, HopRoute{}, cfg.Agent.Kind, "")
			if built.MatchedAt != ResolvedFromRule || built.RuleName == review.Name {
				t.Fatalf("%s: %s builds on %+v, want a rule other than %s", name, issue.Identifier, built, review.Name)
			}
			if chain := cfg.Dispatch.ChainPath(built.RuleName); len(chain) != 2 || chain[1] != review.Name {
				t.Errorf("%s: %s's chain = %v, want [%s %s]", name, issue.Identifier, chain, built.RuleName, review.Name)
			}
		}

		amCfg, err := BuildAutoMergeReactionConfig(cfg.Reactions["auto_merge"])
		if err != nil {
			t.Fatal(err)
		}
		f := reviewExit(t, cfg, review, []string{config.ReviewUILabel})
		f.exit()
		state := NewState(5000, 4, 0, nil, AgentTotals{})
		key := ReactionKey(hopExitIssueID, ReactionKindAutoMerge)
		pending := f.state.PendingReactions[key]
		if pending == nil {
			t.Fatalf("%s: no auto-merge watch after the review; reactions = %v", name, f.state.PendingReactions)
		}
		p := *pending
		state.PendingReactions[key] = &p
		tracker := &uiMergeTracker{labelTracker: labelTracker{labels: []string{config.ReviewUILabel, review.Stage}}}
		pass := func(conversation []domain.ReviewComment, now time.Time) int {
			merges := 0
			state.PendingReactions[key].PendingRetryAt = time.Time{}
			params := autoMergeParams(&reviewReconcileStore{}, uiMergeForge(&merges), tracker)
			params.AutoMergeConfig = amCfg
			params.NowFunc = func() time.Time { return now }
			params.ReviewGate = planReviewGate(cfg, tracker, verdictsOf(f))
			params.ClickProofs = planClickProofs(cfg, &commentForge{conversation: conversation})
			reconcileAutoMerge(state, params, discardLogger(), context.Background(), newAutoMergeMetricsSpy())
			state.TrackerOpsWg.Wait()
			return merges
		}

		// The preview is up, and its review has not recorded anything yet.
		kit := domain.ReviewComment{ID: "issuecomment-1", Reviewer: "brian", Body: previewComment(uiHeadSHA)}
		if merges := pass([]domain.ReviewComment{kit}, time.Now().Add(time.Hour)); merges != 0 {
			t.Fatalf("%s: Sortie merged a ui story with no click-proofs record", name)
		}
		if _, kept := state.PendingReactions[key]; !kept {
			t.Fatalf("%s: auto-merge dropped its watch on a ui story, want it waiting for the click-proofs", name)
		}
		// Still waiting a day later: the kit's merge watch has no window.
		if merges := pass([]domain.ReviewComment{kit}, time.Now().Add(26*time.Hour)); merges != 0 {
			t.Fatalf("%s: Sortie merged a ui story with no click-proofs record", name)
		}
		for _, c := range tracker.written() {
			if strings.Contains(strings.ToLower(c), "approve") {
				t.Errorf("%s: Sortie asks someone to approve a ui story: %q", name, c)
			}
		}

		// The review on the preview records the click-proofs for the head.
		record := domain.ReviewComment{ID: "issuecomment-2", Reviewer: "brian",
			Body: ClickProofsMarker + " " + uiHeadSHA + "\n\n- The worker column: shows each worker. Pass."}
		if merges := pass([]domain.ReviewComment{kit, record}, time.Now().Add(27*time.Hour)); merges != 1 {
			t.Errorf("%s: merges = %d once the click-proofs are recorded for the head, want 1", name, merges)
		}

		for _, rule := range []string{"build", "claude-label", "claude-title"} {
			gate := planReviewGate(cfg, &labelTracker{labels: []string{config.ReviewUILabel}}, allMet{})
			if ok, _, err := gate(context.Background(), &PendingReaction{IssueID: "u", RuleName: rule}); ok || err != nil {
				t.Errorf("%s: gate after %s = %v, %v, want refused: only the review lets a ui story merge", name, rule, ok, err)
			}
		}
	}
}

// The review prompt of a ui story says that its click-proofs come on the
// preview, after it, and that Sortie merges it once they are recorded;
// another story's says nothing of them.
func TestStage0_TheReviewPromptOfAUIStory(t *testing.T) {
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		text := renderUI(t, w, 1, nil)
		wantPrompt(t, name, text, "You are reviewing the pull request of story **ONS-97**",
			"Check every acceptance criterion above",
			"This is a ui story (`sortie-review:ui`): the operator approved its interface as a design prototype before it was written",
			"Check that the branch builds what the approved prototype shows.",
			"That review clicks every action the criteria name on the story's preview and, when they pass, comments `sortie:click-proofs passed <head sha>` on the pull request",
			"Sortie merges it once that record names its head, and no person approves or merges it.",
			"write no such record in this run",
			"Sortie merges the pull request once CI passes and the click-proofs on its preview are recorded.")

		other := renderFixTurn(t, w, 1, nil)
		if !strings.Contains(other, "You are reviewing the pull request of story **ONS-41**") {
			t.Fatalf("%s: the criteria story's review prompt = %q", name, other)
		}
		for _, never := range []string{"click-proofs", "prototype"} {
			if strings.Contains(other, never) {
				t.Errorf("%s: a criteria story's review prompt holds %q", name, never)
			}
		}
	}
}

// Scenario: A built UI story. The kit's comment with its preview's address
// starts a run on the review rule whose prompt is the review on the
// preview: it clicks every action the criteria name there and, when they
// pass, records `sortie:click-proofs passed <head sha>` on the pull request.
func TestStage0_ThePreviewCommentStartsTheReviewOnThePreview(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	rc := w.cfg.Reactions["review_comments"]
	if rc.Provider != "github" {
		t.Fatalf("reactions.review_comments.provider = %q, want github", rc.Provider)
	}
	reviewCfg, err := BuildReviewReactionConfig(rc)
	if err != nil {
		t.Fatalf("review_comments: %v", err)
	}
	if !reviewCfg.Triage.Enabled() {
		t.Error("review_comments has no triage: a live-proof job's result would start a fix run")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Log("no node on PATH: the triage of the preview comment is not run")
	} else if got := runTriage(t, []map[string]any{{"id": "issuecomment-9", "reviewer": "brian", "body": previewComment(uiHeadSHA)}}); got != "dispatch-agent" {
		t.Errorf("triage of the preview comment = %q, want dispatch-agent: it must reach the agent", got)
	}

	// The review's passing exit starts the watch on the pull request.
	f := reviewExit(t, w.cfg, w.review, []string{config.ReviewUILabel})
	f.params.ReviewReactionConfigured = true
	f.exit()
	if _, ok := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindReview)]; !ok {
		t.Fatalf("no review watch after the ui story's review; reactions = %v", f.state.PendingReactions)
	}

	// The triage runs in the machine; here the comment goes to the agent.
	reviewCfg.Triage = config.ReactionTriageConfig{}
	state := stateWithReviewReaction(t, "UI-1", 80)
	state.PendingReactions[ReactionKey("UI-1", ReactionKindReview)].RuleName = w.review.Name
	forge := &commentForge{conversation: []domain.ReviewComment{{
		ID: "issuecomment-9", Reviewer: "brian", Body: previewComment(uiHeadSHA),
		SubmittedAt: reviewBaseTime.Add(-5 * time.Minute),
	}}}
	tracker := &labelTracker{labels: []string{config.ReviewUILabel, w.review.Stage}}
	params := reviewParams(&reviewReconcileStore{}, forge, tracker)
	params.ReviewConfig = reviewCfg
	params.OperatorComments = planOperatorComments(w.cfg, tracker, forge)
	reconcileReviewComments(state, params, discardLogger(), context.Background(), newReviewMetricsSpy())

	retry := state.RetryAttempts["UI-1"]
	if retry == nil || retry.ReactionKind != ReactionKindReview {
		t.Fatalf("scheduled run = %+v, want the review on the preview", retry)
	}
	if retry.RuleName != w.review.Name {
		t.Errorf("run rule = %q, want %s", retry.RuleName, w.review.Name)
	}
	text := renderUI(t, w, 1, retry.ContinuationContext)
	wantPrompt(t, w.name, text, clickProofsPrompt...)
	wantPrompt(t, w.name, text, uiHeadSHA, "ortusnova/agentic-orchestration#80")
	for _, never := range []string{"You are fixing", "You are working on story", "The other comments on the pull request",
		"approve the story", "operator's merge"} {
		if strings.Contains(text, never) {
			t.Errorf("click-proofs prompt holds %q", never)
		}
	}

	// A later turn of the run continues it.
	if next := renderUI(t, w, 2, retry.ContinuationContext); !strings.Contains(next, "Continue with the instructions you received at the start") {
		t.Errorf("turn 2 = %q, want the continuation", next)
	}
}

// The review on the preview takes the newest preview comment and lists the
// operator's comments beside it, which it handles first.
func TestStage0_TheReviewOnThePreviewHandlesTheOperatorsComments(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	text := renderUI(t, w, 1, reviewCommentsOf(
		domain.ReviewComment{ID: "issuecomment-1", Reviewer: "brian", Body: previewComment("1111111111111111111111111111111111111111")},
		domain.ReviewComment{ID: "issuecomment-2", Reviewer: "brian", Body: "The worker column is missing."},
		domain.ReviewComment{ID: "issuecomment-3", Reviewer: "brian", Body: previewComment(uiHeadSHA)},
	))
	wantPrompt(t, w.name, text, clickProofsPrompt...)
	wantPrompt(t, w.name, text, "The other comments on the pull request, oldest first",
		`- brian: <comment>"The worker column is missing."</comment>`,
		"Make each change a comment above asks for that the branch does not hold yet", uiHeadSHA)
	if strings.Contains(text, "Preview of ONS-97 at `1111111111111111111111111111111111111111`") {
		t.Error("the prompt holds an earlier preview's comment, want the newest only")
	}
}

// Scenario: An operator's comment on the ui story's pull request starts a
// review fix run on the branch, whose prompt carries the comment and the
// kit's fix rules, and says Sortie merges once the click-proofs of the new
// head are recorded.
func TestStage0_AnOperatorCommentStartsAFixRun(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	reviewCfg, err := BuildReviewReactionConfig(w.cfg.Reactions["review_comments"])
	if err != nil {
		t.Fatalf("review_comments: %v", err)
	}
	// The triage runs in the machine; here the comment goes to Sortie.
	reviewCfg.Triage = config.ReactionTriageConfig{}
	state := stateWithReviewReaction(t, "UI-1", 80)
	state.PendingReactions[ReactionKey("UI-1", ReactionKindReview)].RuleName = w.review.Name
	forge := &commentForge{conversation: []domain.ReviewComment{{
		ID: "issuecomment-9", Reviewer: "brian", Body: "The worker column is missing.",
		SubmittedAt: reviewBaseTime.Add(-5 * time.Minute),
	}}}
	tracker := &labelTracker{labels: []string{config.ReviewUILabel, w.review.Stage}}
	params := reviewParams(&reviewReconcileStore{}, forge, tracker)
	params.ReviewConfig = reviewCfg
	params.OperatorComments = planOperatorComments(w.cfg, tracker, forge)
	reconcileReviewComments(state, params, discardLogger(), context.Background(), newReviewMetricsSpy())

	retry := state.RetryAttempts["UI-1"]
	if retry == nil || retry.ReactionKind != ReactionKindReview {
		t.Fatalf("scheduled run = %+v, want a review fix run", retry)
	}
	if retry.RuleName != w.review.Name {
		t.Errorf("fix run rule = %q, want %s", retry.RuleName, w.review.Name)
	}
	text := renderFix(t, w, retry.ContinuationContext)
	wantPrompt(t, w.name, text, fixPromptRules...)
	wantPrompt(t, w.name, text, "The operator looked at the pull request, or at its preview, and asks for a change.",
		`- brian: <comment>"The worker column is missing."</comment>`,
		"the preview follows the branch once CI passes",
		"Sortie merges the pull request once that review records the click-proofs for the new head: no person approves it.")
	for _, never := range []string{"You are reviewing", "You are working on story", "CI fails on", "conflicts with its base branch",
		"operator's merge", "Sortie does not merge this pull request"} {
		if strings.Contains(text, never) {
			t.Errorf("operator fix prompt holds %q", never)
		}
	}
}

// A click-proofs record on the pull request is no request for a change: it
// starts no run.
func TestStage0_TheClickProofsRecordStartsNoRun(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	reviewCfg, err := BuildReviewReactionConfig(w.cfg.Reactions["review_comments"])
	if err != nil {
		t.Fatalf("review_comments: %v", err)
	}
	// The triage runs in the machine; here the comment goes to Sortie.
	reviewCfg.Triage = config.ReactionTriageConfig{}
	state := stateWithReviewReaction(t, "UI-1", 80)
	state.PendingReactions[ReactionKey("UI-1", ReactionKindReview)].RuleName = w.review.Name
	forge := &commentForge{conversation: []domain.ReviewComment{{
		ID: "issuecomment-9", Reviewer: "brian", Body: ClickProofsMarker + " " + uiHeadSHA + "\n\n- The worker column: pass.",
		SubmittedAt: reviewBaseTime.Add(-5 * time.Minute),
	}}}
	tracker := &labelTracker{labels: []string{config.ReviewUILabel, w.review.Stage}}
	params := reviewParams(&reviewReconcileStore{}, forge, tracker)
	params.ReviewConfig = reviewCfg
	params.OperatorComments = planOperatorComments(w.cfg, tracker, forge)
	reconcileReviewComments(state, params, discardLogger(), context.Background(), newReviewMetricsSpy())

	if retry := state.RetryAttempts["UI-1"]; retry != nil {
		t.Errorf("scheduled run = %+v after a click-proofs record, want none", retry)
	}
}

// Sortie's merge reaches the story: merge completion moves it to Done.
func TestStage0_TheMergeMakesAUIStoryDone(t *testing.T) {
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		mcCfg, err := BuildMergeCompletionReactionConfig(cfg.Reactions["merge_completion"], cfg.Tracker)
		if err != nil {
			t.Fatal(err)
		}
		f := reviewExit(t, cfg, review, []string{config.ReviewUILabel})
		f.exit()
		mc := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindMergeCompletion)]
		if mc == nil {
			t.Fatalf("%s: no merge completion watch on a ui story", name)
		}
		state := NewState(5000, 4, 0, nil, AgentTotals{})
		state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindMergeCompletion)] = mc
		tracker := &mgcTrackerFake{states: map[string]string{hopExitIssueID: cfg.Tracker.HandoffState}}
		forge := &mgcSCMFake{fn: func(int, string, string) (domain.PRMergeStatus, error) { return mergedStatus("merge-97"), nil }}
		params := mgcParams(newMGCStore(), forge, tracker)
		params.HandoffState = cfg.Tracker.HandoffState
		params.TerminalStates = cfg.Tracker.TerminalStates
		params.MergeCompletionConfig = mcCfg
		params.NowFunc = func() time.Time { return time.Now().Add(time.Hour) }
		reconcileMergeCompletion(state, params, discardLogger(), context.Background(), &domain.NoopMetrics{})
		if len(tracker.transitionCalls) != 1 || tracker.transitionCalls[0].target != "Done" {
			t.Errorf("%s: transitions = %+v, want one to Done", name, tracker.transitionCalls)
		}
	}
}
