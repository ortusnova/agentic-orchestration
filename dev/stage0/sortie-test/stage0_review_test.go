// The stage-0 kit's review gate (agent-platform/run-supervision#Every pull
// request is reviewed by an agent and merged by Sortie), checked against
// Sortie itself. dev/stage0/test-review-gate.sh compiles this file into
// Sortie's orchestrator package with `go test -overlay`, so it loads the
// kit's workflows with Sortie's own loader and dispatch validation, and
// drives Sortie's exit, auto-merge and merge-completion code with Sortie's
// test fakes for the tracker and the forge. STAGE0_KIT names the kit
// directory (dev/stage0).

package orchestrator

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/workflow"
	"github.com/sortie-ai/sortie/internal/workspace"
)

// One Sortie, one workflow (agent-platform-workers D18): the homelab profile is retired.
var stage0Workflows = []string{"WORKFLOW.md"}

// reviewHuman is the label plan sync keeps on a story with review: human.
const reviewHuman = "sortie-review:human"

// stage0Values stand in for what stage0.sh renders into the workflow.
var stage0Values = map[string]string{
	"STAGE0_SORTIE_PORT":       "7680",
	"STAGE0_SORTIE_MAX_AGENTS": "16",
	"STAGE0_HOMELAB_MOUNTS":    "[]",
	"STAGE0_HULY_URL":          "http://127.0.0.1:8087",
	// No operator channel; stage0_notify_test.go renders each kind.
	"STAGE0_NOTIFY_ENTRY": "",
	// The remote kind's flow URLs: Pomerium's public URL (control-plane/).
	"STAGE0_CP_PUBLIC_URL": "https://p1.example.org:8443",

	"STAGE0_AO_REPO_PROJECT":      "ortusnova/agentic-orchestration",
	"STAGE0_HOMELAB_REPO_PROJECT": "ortusnova/homelab",
}

var stage0Placeholder = regexp.MustCompile(`\$\{(STAGE0_[A-Z_]+)\}`)

// loadStage0 renders one of the kit's workflows and loads it the way Sortie
// does at start and on reload, dispatch validation included.
func loadStage0(t *testing.T, name string) config.ServiceConfig {
	t.Helper()
	return loadStage0Workflow(t, name).Config()
}

// loadStage0Workflow is loadStage0, returning Sortie's workflow manager,
// which also holds the parsed prompt template.
func loadStage0Workflow(t *testing.T, name string) *workflow.Manager {
	t.Helper()
	kit := os.Getenv("STAGE0_KIT")
	if kit == "" {
		t.Fatal("STAGE0_KIT is not set: run dev/stage0/test-review-gate.sh")
	}
	raw, err := os.ReadFile(filepath.Join(kit, name))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	text := stage0Placeholder.ReplaceAllStringFunc(string(raw), func(m string) string {
		key := stage0Placeholder.FindStringSubmatch(m)[1]
		if v, ok := stage0Values[key]; ok {
			return v
		}
		if strings.HasSuffix(key, "_URL") {
			return "https://github.com/ortusnova/" + strings.ToLower(key) + ".git"
		}
		if key == "STAGE0_HOME" || key == "STAGE0_PACK" {
			return filepath.Join(dir, strings.ToLower(key))
		}
		return strings.ToLower(key)
	})
	if err := os.WriteFile(filepath.Join(dir, "stage0_pack"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "WORKFLOW.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	// The workbench's notifications endpoint, as run-sortie.sh puts it in
	// Sortie's environment (stage0_notify_test.go).
	t.Setenv("SORTIE_WORKBENCH_NOTIFY_URL", stage0Workbench.url())
	// The supervisor's prompt, beside the workflow, as stage0.sh installs it.
	if prompt, err := os.ReadFile(filepath.Join(kit, "supervisor.md")); err == nil {
		if err := os.WriteFile(filepath.Join(dir, "supervisor.md"), prompt, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mgr, err := workflow.NewManager(path, discardLogger())
	if err != nil {
		t.Fatalf("Sortie refused %s: %v", name, err)
	}
	cfg := mgr.Config()
	if !cfg.Tracker.PlanControl() {
		t.Fatalf("%s: plan control is off", name)
	}
	if err := config.ValidatePlanReviewGates(cfg.Dispatch, true); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return mgr
}

// stage0Review is the one rule of the workflow that carries gate: criteria.
func stage0Review(t *testing.T, cfg config.ServiceConfig) config.DispatchRule {
	t.Helper()
	var gates []config.DispatchRule
	for _, r := range cfg.Dispatch.Rules {
		if r.Gate == config.GateCriteria {
			gates = append(gates, r)
		}
	}
	if len(gates) != 1 || gates[0].Stage == "" {
		t.Fatalf("criteria rules = %+v, want one, with a stage label", gates)
	}
	return gates[0]
}

// stage0Stories are the kinds of story the kit runs: each review value, on
// each harness.
var stage0Stories = []domain.Issue{
	{ID: "h", Identifier: "ONS-41", Title: "Count the stories", Labels: []string{reviewHuman}},
	{ID: "c", Identifier: "ONS-42", Title: "Count the stories", Labels: []string{config.ReviewCriteriaLabel}},
	{ID: "hc", Identifier: "ONS-43", Title: "Count the stories", Labels: []string{reviewHuman, "claude-code"}},
	{ID: "ct", Identifier: "ONS-44", Title: "[claude] Count the stories", Labels: []string{config.ReviewCriteriaLabel}},
}

// Every story, whatever its review value, is built by a rule that is not
// the review, and its chain ends in the review: a stage hop, so the review
// is a new run in a new agent session.
func TestStage0_EveryStoryGoesThroughTheReview(t *testing.T) {
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		builders := map[string]bool{}
		for _, issue := range stage0Stories {
			built := ResolveRule(issue, cfg.Dispatch, HopRoute{}, cfg.Agent.Kind, "")
			if built.MatchedAt != ResolvedFromRule || built.RuleName == review.Name {
				t.Fatalf("%s: %s builds on %+v, want a rule other than %s", name, issue.Identifier, built, review.Name)
			}
			builders[built.RuleName] = true
			chain := cfg.Dispatch.ChainPath(built.RuleName)
			if len(chain) != 2 || chain[1] != review.Name {
				t.Errorf("%s: %s's chain = %v, want [%s %s]", name, issue.Identifier, chain, built.RuleName, review.Name)
			}
			hopped := issue
			hopped.Labels = append(slices.Clone(issue.Labels), review.Stage)
			after := ResolveRule(hopped, cfg.Dispatch, HopRoute{TargetRule: review.Name, TargetLabel: review.Stage}, cfg.Agent.Kind, "")
			if after.RuleName != review.Name {
				t.Errorf("%s: %s after its build runs %q, want %s", name, issue.Identifier, after.RuleName, review.Name)
			}
		}
		if len(builders) != 3 {
			t.Errorf("%s: builders = %v, want the opencode one and both claude ones", name, builders)
		}
		if review.OnFail != config.OnFailEscalate {
			t.Errorf("%s: review on_fail = %q, want escalate: a criterion it cannot settle goes to a person", name, review.OnFail)
		}
	}
}

// The review's prompt says to check every criterion with evidence, to add
// fix commits and never rewrite a pushed one, and to stop blocked on a
// criterion it cannot settle.
func TestStage0_ReviewPrompt(t *testing.T) {
	kit := os.Getenv("STAGE0_KIT")
	for _, name := range stage0Workflows {
		raw, err := os.ReadFile(filepath.Join(kit, name))
		if err != nil {
			t.Fatal(err)
		}
		body := string(raw)
		start := strings.Index(body, `{{- else if eq .stage.current "review" -}}`)
		end := strings.Index(body, "{{- else -}}")
		if start < 0 || end < start {
			t.Fatalf("%s: no review prompt", name)
		}
		prompt := strings.Join(strings.Fields(body[start:end]), " ")
		for _, want := range []string{
			"Another agent session built this story",
			"Check every acceptance criterion",
			"with evidence",
			"add commits on top of the branch",
			"Never rewrite a commit that is already pushed",
			"do not amend, rebase, squash, reset, or drop",
			"stop with `blocked`",
			"conflicts with a documented design rule",
			"Sortie keeps the pull request open",
			"record your verdict on every acceptance criterion in `.sortie/criteria.json`",
			`"verdict": "met", "evidence":`,
			"`not-met`: it does not hold, and you could not meet it.",
			"`live-proof`: only a live run this machine cannot do settles it.",
			"`unsettled`: it conflicts with a documented design rule",
			"A criterion you could not prove here is not `met`",
			"a step that timed out, or the builder's claim is not evidence",
			"Sortie merges the pull request only when every criterion is `met`",
			"no `.sortie/criteria.json`, keeps the pull request open",
			"record it `not-met` with what you ran and what failed",
			"record it `unsettled` and stop with `blocked`",
		} {
			if !strings.Contains(prompt, want) {
				t.Errorf("%s: review prompt lacks %q", name, want)
			}
		}
	}
}

// reviewExit is the exit of a story's review run on the kit's workflow, with
// its pull request recorded and both merge reactions configured.
func reviewExit(t *testing.T, cfg config.ServiceConfig, review config.DispatchRule, labels []string) *hopExitFixture {
	t.Helper()
	f := newHopExitFixture(t)
	f.params.PlanReleaseState = cfg.Tracker.ReleaseState
	f.params.Dispatch = cfg.Dispatch
	f.params.HandoffState = cfg.Tracker.HandoffState
	f.params.ActiveStates = cfg.Tracker.ActiveStates
	f.params.TerminalStates = cfg.Tracker.TerminalStates
	entry := f.state.Running[hopExitIssueID]
	entry.RuleName = review.Name
	entry.Issue.State = cfg.Tracker.InProgressState
	entry.Issue.Labels = append(slices.Clone(labels), review.Stage)
	entry.DispatchLabels = slices.Clone(entry.Issue.Labels)
	ws := t.TempDir()
	writePRSCMMetadata(t, ws, 41, "ortusnova", "agentic-orchestration", "sortie/ONS-41", "deadbeef")
	f.result.WorkspacePath = ws
	f.params.SCMAdapter = &scmAdapterStubExit{}
	f.params.AutoMergeReactionConfigured = true
	f.params.MergeCompletionReactionConfigured = true
	// The review records every criterion met, as the kit's review prompt
	// asks a passing review to.
	f.result.CriteriaFile = []byte(stage0AllMet)
	f.params.Store = &verdictExitStore{mockExitStore: f.store, verdictStore: &verdictStore{}}
	return f
}

// stage0AllMet is the .sortie/criteria.json of a review that found every
// criterion met.
const stage0AllMet = `{"criteria": [{"criterion": "make test passes", "verdict": "met", "evidence": "make test: 12 passed"}]}`

// verdictsOf is the verdict store a review exit recorded into.
func verdictsOf(f *hopExitFixture) *verdictStore {
	return f.params.Store.(*verdictExitStore).verdictStore
}

// Scenario: A pull request that meets its criteria. Its review passes, CI
// passes, and Sortie merges it and marks the story Done, with no person
// involved, whatever the story's review value.
func TestStage0_APullRequestThatMeetsItsCriteria(t *testing.T) {
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		amCfg, err := BuildAutoMergeReactionConfig(cfg.Reactions["auto_merge"])
		if err != nil || cfg.Reactions["auto_merge"].Provider == "" {
			t.Fatalf("%s: auto_merge = %+v, %v, want it configured", name, cfg.Reactions["auto_merge"], err)
		}
		if !amCfg.RequireCI {
			t.Errorf("%s: auto_merge does not require CI", name)
		}
		mcCfg, err := BuildMergeCompletionReactionConfig(cfg.Reactions["merge_completion"], cfg.Tracker)
		if err != nil || mcCfg.TargetState != cfg.Tracker.DoneState {
			t.Fatalf("%s: merge_completion = %+v, %v, want it to move the story to %s", name, mcCfg, err, cfg.Tracker.DoneState)
		}
		for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
			t.Run(name+"/"+value, func(t *testing.T) {
				// The review run ends normally: no status file, and every
				// criterion recorded met.
				f := reviewExit(t, cfg, review, []string{value})
				f.exit()
				pending := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)]
				if pending == nil {
					t.Fatalf("no auto-merge after a passing review; reactions = %v", f.state.PendingReactions)
				}
				if pending.RuleName != review.Name {
					t.Errorf("auto-merge rule = %q, want %s", pending.RuleName, review.Name)
				}

				for _, ci := range []string{"failure", "pending", "success"} {
					merged := false
					scm := &controlledSCMAdapter{
						getMergeabilityFn: func(context.Context, int, string, string) (domain.PRMergeStatus, error) {
							return domain.PRMergeStatus{Mergeability: domain.MergeabilityClean, HeadSHA: "deadbeef"}, nil
						},
						getReviewDecisionFn: func(context.Context, int, string, string) (domain.ReviewDecision, error) {
							return domain.ReviewDecisionNotRequired, nil
						},
						getCIStatusFn: func(context.Context, int, string, string) (string, error) { return ci, nil },
						mergePRFn: func(context.Context, int, string, string, domain.MergeStrategy, string, string, string) (domain.MergeResult, error) {
							merged = true
							return domain.MergeResult{SHA: "merge-41"}, nil
						},
					}
					state := NewState(5000, 4, 0, nil, AgentTotals{})
					p := *pending
					state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)] = &p
					params := autoMergeParams(&reviewReconcileStore{}, scm, &reviewTrackerStub{})
					params.AutoMergeConfig = amCfg
					params.NowFunc = func() time.Time { return time.Now().Add(time.Hour) }
					params.ReviewGate = planReviewGate(cfg, &labelTracker{labels: []string{value, review.Stage}}, verdictsOf(f))
					reconcileAutoMerge(state, params, discardLogger(), context.Background(), newAutoMergeMetricsSpy())
					state.TrackerOpsWg.Wait()
					if merged != (ci == "success") {
						t.Errorf("CI %s: merged = %v", ci, merged)
					}
				}

				// The merge reaches the story: merge completion moves it to Done.
				mc := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindMergeCompletion)]
				if mc == nil {
					t.Fatal("no merge completion after a passing review")
				}
				state := NewState(5000, 4, 0, nil, AgentTotals{})
				state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindMergeCompletion)] = mc
				tracker := &mgcTrackerFake{states: map[string]string{hopExitIssueID: cfg.Tracker.HandoffState}}
				forge := &mgcSCMFake{fn: func(int, string, string) (domain.PRMergeStatus, error) { return mergedStatus("merge-41"), nil }}
				params := mgcParams(newMGCStore(), forge, tracker)
				params.HandoffState = cfg.Tracker.HandoffState
				params.TerminalStates = cfg.Tracker.TerminalStates
				params.MergeCompletionConfig = mcCfg
				params.NowFunc = func() time.Time { return time.Now().Add(time.Hour) }
				reconcileMergeCompletion(state, params, discardLogger(), context.Background(), &domain.NoopMetrics{})
				if len(tracker.transitionCalls) != 1 || tracker.transitionCalls[0].target != "Done" {
					t.Errorf("transitions = %+v, want one to Done", tracker.transitionCalls)
				}
			})
		}
	}
}

// The build's own run never merges: it hops to the review, and its
// self-review is not the gate.
func TestStage0_TheBuildDoesNotMerge(t *testing.T) {
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
			gate := planReviewGate(cfg, &labelTracker{labels: []string{value}}, allMet{})
			for _, builder := range []string{"build", "claude-label", "claude-title"} {
				ok, _, err := gate(context.Background(), &PendingReaction{IssueID: "i", RuleName: builder})
				if ok || err != nil {
					t.Errorf("%s: %s %s: gate = %v, %v, want refused", name, value, builder, ok, err)
				}
			}
			ok, why, err := gate(context.Background(), &PendingReaction{IssueID: "i", RuleName: review.Name})
			if !ok || err != nil {
				t.Errorf("%s: %s after the review: gate = %v (%s), %v, want allowed", name, value, ok, why, err)
			}
		}
	}
}

// Scenario: A criterion the review cannot settle. The review stops blocked
// on a conflict with a documented design rule: the pull request is not
// merged, and the story is parked with the review's question, which is what
// the workbench shows as a question.
func TestStage0_ACriterionTheReviewCannotSettle(t *testing.T) {
	const question = "Criterion 2 conflicts with design D5 (docs/design.md): it asks a person to merge. Which one holds?"
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
			t.Run(name+"/"+value, func(t *testing.T) {
				f := reviewExit(t, cfg, review, []string{value})
				f.state.Running[hopExitIssueID].LastAgentMessage = question
				f.result.SoftStop = true
				f.result.SoftStopReason = string(workspace.StatusBlocked)
				f.result.SoftStopStatement = workspace.StatusStatement{Text: question}
				f.exit()

				if _, ok := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)]; ok {
					t.Error("auto-merge seeded after a review that could not settle a criterion")
				}
				parked := RuntimeSnapshot(f.state, time.Now()).Parked
				if len(parked) != 1 || parked[0].IssueID != hopExitIssueID || !strings.Contains(parked[0].Statement, "design D5") {
					t.Errorf("parked = %+v, want the story parked with the review's question", parked)
				}
				commented := false
				for _, c := range f.tracker.commentCalls {
					commented = commented || (strings.Contains(c.Text, "not merged") && strings.Contains(c.Text, "Criterion 2"))
				}
				if !commented {
					t.Errorf("comments = %+v, want one naming criterion 2", f.tracker.commentCalls)
				}
			})
		}
	}
}

// Scenario: A criterion the review found not met (ONS-19). The review ran
// what it could, found the full vm chain not proven, and ended normally,
// with no status file, recording the criterion not-met: Sortie does not
// merge the pull request, and parks the story with the criterion as the
// supervisor's question. A review that records no verdict at all is held
// the same way.
func TestStage0_ACriterionTheReviewFoundNotMet(t *testing.T) {
	const unmet = `{"criteria": [
		{"criterion": "Run by the one Sortie on taichi's worker, the full vm chain in its machine ends DONE with every step at exit code 0", "verdict": "not-met", "evidence": "seed-vm-recreate timed out after 30 min: the nested VMs have no network"},
		{"criterion": "make test passes", "verdict": "met", "evidence": "make test: 4846 passed, 24 skipped"}]}`
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		review := stage0Review(t, cfg)
		for _, file := range []string{unmet, ""} {
			for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
				t.Run(name+"/"+value, func(t *testing.T) {
					f := reviewExit(t, cfg, review, []string{value})
					f.state.Running[hopExitIssueID].LastAgentMessage = "No acceptance criterion is fully proven yet. I added no commits and wrote no `.sortie/status`."
					f.result.CriteriaFile = []byte(file)
					f.exit()

					if _, ok := f.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)]; ok {
						t.Error("auto-merge seeded after a review that found a criterion not met")
					}
					want := "no verdict per criterion"
					if file != "" {
						want = "seed-vm-recreate timed out"
					}
					parked := RuntimeSnapshot(f.state, time.Now()).Parked
					if len(parked) != 1 || !strings.Contains(parked[0].Statement, want) {
						t.Errorf("parked = %+v, want the story parked with %q as the question", parked, want)
					}
					gate := planReviewGate(cfg, &labelTracker{labels: []string{value, review.Stage}}, verdictsOf(f))
					if ok, why, err := gate(context.Background(), &PendingReaction{IssueID: hopExitIssueID, RuleName: review.Name}); ok || err != nil {
						t.Errorf("gate = %v (%s), %v; want the merge refused", ok, why, err)
					}
				})
			}
		}
	}
}
