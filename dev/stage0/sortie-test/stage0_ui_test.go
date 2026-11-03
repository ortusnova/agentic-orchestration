// The stage-0 kit's ui stories (agent-platform/run-supervision#A UI story
// is built before it is approved, and its merge is the approval), checked
// against Sortie itself as stage0_review_test.go checks the review gate:
// a sortie-review:ui story goes through the review and is never
// auto-merged; on p1 the operator's comment on its pull request starts a
// fix run with the kit's fix prompt, and the kit's own preview comment does
// not; the operator's merge makes it Done.

package orchestrator

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
)

// previewMarker opens the kit's comment with a preview's address
// (workbench/src/workbench/preview.py).
const previewMarker = "<!-- workbench-preview -->"

// Scenario: A UI story from spec to merge. It is built by a rule that is
// not the review, its chain ends in the review, and after a passing review
// with clean, green CI Sortie still does not merge it.
func TestStage0_AUIStoryIsReviewedAndNeverAutoMerged(t *testing.T) {
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
		// The passing review seeds the merge watch; the gate is what refuses it.
		pending := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)]
		if pending == nil {
			t.Fatalf("%s: no auto-merge watch after the review; reactions = %v", name, f.state.PendingReactions)
		}
		p := *pending
		state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)] = &p
		merged := false
		scm := &controlledSCMAdapter{
			getMergeabilityFn: func(context.Context, int, string, string) (domain.PRMergeStatus, error) {
				return domain.PRMergeStatus{Mergeability: domain.MergeabilityClean, HeadSHA: "deadbeef"}, nil
			},
			getReviewDecisionFn: func(context.Context, int, string, string) (domain.ReviewDecision, error) {
				return domain.ReviewDecisionApproved, nil
			},
			getCIStatusFn: func(context.Context, int, string, string) (string, error) { return "success", nil },
			mergePRFn: func(context.Context, int, string, string, domain.MergeStrategy, string, string, string) (domain.MergeResult, error) {
				merged = true
				return domain.MergeResult{SHA: "merge-97"}, nil
			},
		}
		params := autoMergeParams(&reviewReconcileStore{}, scm, &reviewTrackerStub{})
		params.AutoMergeConfig = amCfg
		params.NowFunc = func() time.Time { return time.Now().Add(time.Hour) }
		params.ReviewGate = planReviewGate(cfg, &labelTracker{labels: []string{config.ReviewUILabel, review.Stage}})
		reconcileAutoMerge(state, params, discardLogger(), context.Background(), newAutoMergeMetricsSpy())
		state.TrackerOpsWg.Wait()
		if merged {
			t.Errorf("%s: Sortie merged a ui story", name)
		}
		if _, kept := state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)]; kept {
			t.Errorf("%s: auto-merge kept its watch on a ui story, want it dropped: the operator merges", name)
		}
		for _, rule := range []string{review.Name, "build", "claude-label", "claude-title"} {
			gate := planReviewGate(cfg, &labelTracker{labels: []string{config.ReviewUILabel}})
			if ok, _, err := gate(context.Background(), &PendingReaction{IssueID: "u", RuleName: rule}); ok || err != nil {
				t.Errorf("%s: gate after %s = %v, %v, want refused", name, rule, ok, err)
			}
		}
	}
}

// Scenario: The operator asks for a change. On p1, a comment on the ui
// story's pull request starts a review fix run on the branch, whose prompt
// carries the comment and the kit's fix rules.
func TestStage0_AnOperatorCommentStartsAFixRun(t *testing.T) {
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
		t.Error("review_comments has no triage: the kit's preview comment would start a fix run")
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
		`- brian: <comment>"The worker column is missing."</comment>`, "Pass over a comment that starts with",
		"the preview follows the branch once CI passes", "Sortie does not merge this pull request")
	for _, never := range []string{"You are reviewing", "You are working on story", "CI fails on", "conflicts with its base branch"} {
		if strings.Contains(text, never) {
			t.Errorf("operator fix prompt holds %q", never)
		}
	}
}

// The triage passes over the kit's comment with the preview's address,
// alone, and hands every set with an operator's comment to the agent.
func TestStage0_TheTriagePassesOverThePreviewComment(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node on PATH: the machines run the triage with the pack's node")
	}
	script := loadStage0(t, "WORKFLOW.md").Reactions["review_comments"].Triage.Script
	kit := map[string]any{"id": "issuecomment-1", "reviewer": "brian", "body": previewMarker + "\nPreview of ONS-97: https://workbench-ons-97.example/"}
	operator := map[string]any{"id": "issuecomment-2", "reviewer": "brian", "body": "The worker column is missing."}
	for _, tc := range []struct {
		name     string
		comments []map[string]any
		want     string
	}{
		{"the preview comment", []map[string]any{kit}, "handled"},
		{"the operator's comment", []map[string]any{operator}, "dispatch-agent"},
		{"both", []map[string]any{kit, operator}, "dispatch-agent"},
		{"none", nil, "dispatch-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input, result := filepath.Join(dir, "input.json"), filepath.Join(dir, "result.json")
			doc, _ := json.Marshal(map[string]any{"schema_version": 1, "reaction_kind": "review", "subject": tc.comments})
			if err := os.WriteFile(input, doc, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "SORTIE_REACTION_KIND=review", "SORTIE_REACTION_INPUT="+input, "SORTIE_REACTION_RESULT="+result)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("triage: %v: %s", err, out)
			}
			raw, err := os.ReadFile(result)
			if err != nil {
				t.Fatal(err)
			}
			var got struct{ Disposition string }
			if err := json.Unmarshal(raw, &got); err != nil || got.Disposition != tc.want {
				t.Errorf("disposition = %q (%v), want %s", got.Disposition, err, tc.want)
			}
		})
	}
}

// Scenario: The operator is satisfied. Their merge reaches the story:
// merge completion moves it to Done.
func TestStage0_TheOperatorsMergeMakesAUIStoryDone(t *testing.T) {
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
