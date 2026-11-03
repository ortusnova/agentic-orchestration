// The stage-0 kit's fix runs (agent-platform/run-supervision#A pull request
// that fails CI or conflicts is fixed by an agent), checked against Sortie
// itself, as stage0_review_test.go checks the review gate: each workflow
// is loaded with Sortie's loader and validation, its prompt is rendered
// with Sortie's prompt code, and Sortie's exit, CI, merge-conflict and
// auto-merge code runs against Sortie's test fakes for the tracker and the
// forge.

package orchestrator

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/route"
	"github.com/sortie-ai/sortie/internal/prompt"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/scm/github"
)

// fixRepoProject is each workflow's github.project, the repository its CI
// provider is configured with.
var fixRepoProject = map[string]string{
	"WORKFLOW.md": "ortusnova/agentic-orchestration",
}

// stage0Fix is one workflow with the reaction settings Sortie builds from
// it at start.
type stage0Fix struct {
	name   string
	cfg    config.ServiceConfig
	tmpl   *prompt.Template
	review config.DispatchRule
	mc     MergeConflictReactionConfig
	am     AutoMergeReactionConfig
}

// mergeConflicts reports whether Sortie runs the merge_conflicts reaction
// for the workflow, as it decides at start.
func (w stage0Fix) mergeConflicts() bool { return w.cfg.Reactions["merge_conflicts"].Provider != "" }

func loadStage0Fix(t *testing.T, name string) stage0Fix {
	t.Helper()
	mgr := loadStage0Workflow(t, name)
	cfg := mgr.Config()
	mc, err := BuildMergeConflictReactionConfig(cfg.Reactions["merge_conflicts"])
	if err != nil {
		t.Fatalf("%s: merge_conflicts: %v", name, err)
	}
	am, err := BuildAutoMergeReactionConfig(cfg.Reactions["auto_merge"])
	if err != nil {
		t.Fatalf("%s: auto_merge: %v", name, err)
	}
	return stage0Fix{name: name, cfg: cfg, tmpl: mgr.PromptTemplate(), review: stage0Review(t, cfg), mc: mc, am: am}
}

// fixClock is the time every step of a scenario sees; tick moves it past
// every poll interval and backoff.
type fixClock struct{ now time.Time }

func (c *fixClock) Now() time.Time { return c.now }
func (c *fixClock) tick()          { c.now = c.now.Add(time.Hour) }

// fixForge is the fake forge of a scenario: the pull request's head, its
// mergeability against the base, and its CI.
type fixForge struct {
	head   string
	dirty  bool
	ci     domain.CIResult
	merged []string
}

func (f *fixForge) status() domain.PRMergeStatus {
	m := domain.MergeabilityClean
	if f.dirty {
		m = domain.MergeabilityDirty
	}
	return domain.PRMergeStatus{Mergeability: m, HeadSHA: f.head, BranchName: "sortie/ONS-41", BaseBranch: "main"}
}

func (f *fixForge) scm() *mergeabilitySCM {
	return &mergeabilitySCM{fn: func() (domain.PRMergeStatus, error) { return f.status(), nil }}
}

// fixScenario runs one story's pull request through the kit's workflow.
type fixScenario struct {
	t      *testing.T
	w      stage0Fix
	value  string
	clock  *fixClock
	forge  *fixForge
	state  *State
	store  *statefulFingerprintStore
	ci     *mockCIProvider
	issues *opsTracker
	router *route.Router
}

func newFixScenario(t *testing.T, w stage0Fix, value string) *fixScenario {
	t.Helper()
	issues := &opsTracker{mockTrackerAdapter: &mockTrackerAdapter{}}
	stage0Workbench.reset()
	return &fixScenario{
		t:      t,
		w:      w,
		value:  value,
		clock:  &fixClock{now: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)},
		forge:  &fixForge{head: "deadbeef"},
		store:  newStatefulFingerprintStore(),
		ci:     &mockCIProvider{},
		issues: issues,
		// The workflow's own notifications: its tracker_comment entry
		// posts on the story, its webhook entry to the workbench.
		router: mustRouter(t, issues, registry.Notifiers.Get, route.Inputs{Entries: w.cfg.Notifications.Backends}),
	}
}

// exitRun ends a run on the review rule, normally, with the pull request at
// the forge's head: the review run itself when reaction is empty, else a
// fix run that reaction started. It carries the reactions the scenario
// holds over to the exit and returns the run's exit.
func (s *fixScenario) exitRun(reaction string) {
	s.t.Helper()
	f := reviewExit(s.t, s.w.cfg, s.w.review, []string{s.value})
	writePRSCMMetadata(s.t, f.result.WorkspacePath, 41, "ortusnova", "agentic-orchestration", "sortie/ONS-41", s.forge.head)
	if s.w.cfg.CIFeedback.Kind != "" {
		f.params.CIProvider = s.ci
	}
	f.params.MergeConflictReactionConfigured = s.w.mergeConflicts()
	f.params.Router = s.router
	f.params.NowFunc = s.clock.Now
	entry := f.state.Running[hopExitIssueID]
	entry.ReactionKind = reaction
	if s.state != nil {
		CancelRetry(s.state, hopExitIssueID)
		f.state.PendingReactions = s.state.PendingReactions
		f.state.ReactionAttempts = s.state.ReactionAttempts
	}
	f.exit()
	s.state = f.state
	for _, kind := range []string{ReactionKindCI, ReactionKindMergeConflict, ReactionKindAutoMerge} {
		p := s.state.PendingReactions[ReactionKey(hopExitIssueID, kind)]
		if p == nil {
			s.t.Fatalf("after the run: no %s watch; reactions = %v", kind, s.state.PendingReactions)
		}
		if p.RuleName != s.w.review.Name {
			s.t.Errorf("%s watch rule = %q, want %s: its fix run is a review run", kind, p.RuleName, s.w.review.Name)
		}
	}
}

func (s *fixScenario) params() ReconcileParams {
	return ReconcileParams{
		TrackerAdapter:                  s.issues,
		Router:                          s.router,
		SCMAdapter:                      s.forge.scm(),
		CIProvider:                      s.ci,
		CIFeedback:                      s.w.cfg.CIFeedback,
		CIWatchWindow:                   time.Duration(s.w.cfg.CIFeedback.WatchWindowMS) * time.Millisecond,
		MergeConflictConfig:             s.w.mc,
		MergeConflictPendingTTL:         time.Duration(s.w.mc.WatchWindowMS) * time.Millisecond,
		MergeConflictReactionConfigured: s.w.mergeConflicts(),
		Store:                           s.store,
		OnRetryFire:                     noopRetryFire,
		Ctx:                             context.Background(),
		Logger:                          discardLogger(),
		NowFunc:                         s.clock.Now,
	}
}

// checkMergeConflicts and checkCI are one due pass of each watch.
func (s *fixScenario) checkMergeConflicts() {
	s.clock.tick()
	reconcileMergeConflicts(s.state, s.params(), discardLogger(), context.Background(), &domain.NoopMetrics{})
	s.state.TrackerOpsWg.Wait()
}

func (s *fixScenario) checkCI(result domain.CIResult) {
	s.clock.tick()
	result.Ref = s.forge.head
	s.ci.result = result
	reconcileCIStatus(s.state, s.params(), discardLogger(), context.Background(), &domain.NoopMetrics{})
	s.state.TrackerOpsWg.Wait()
}

// fixRun is the run a reaction scheduled: its kind, and the prompt the
// kit's template gives it.
func (s *fixScenario) fixRun(kind string) string {
	s.t.Helper()
	retry := s.state.RetryAttempts[hopExitIssueID]
	if retry == nil || retry.ReactionKind != kind {
		s.t.Fatalf("scheduled run = %+v, want a %s fix run", retry, kind)
	}
	if retry.RuleName != s.w.review.Name {
		s.t.Errorf("fix run rule = %q, want %s", retry.RuleName, s.w.review.Name)
	}
	return renderFix(s.t, s.w, retry.ContinuationContext)
}

// autoMerge is one pass of auto-merge at the forge's head, with the
// workflow's review gate; it reports whether Sortie merged.
func (s *fixScenario) autoMerge(ci string) bool {
	s.t.Helper()
	pending := s.state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)]
	if pending == nil {
		s.t.Fatal("no auto-merge watch")
	}
	merged := false
	scm := &controlledSCMAdapter{
		getMergeabilityFn: func(context.Context, int, string, string) (domain.PRMergeStatus, error) {
			return s.forge.status(), nil
		},
		getReviewDecisionFn: func(context.Context, int, string, string) (domain.ReviewDecision, error) {
			return domain.ReviewDecisionNotRequired, nil
		},
		getCIStatusFn: func(context.Context, int, string, string) (string, error) { return ci, nil },
		mergePRFn: func(context.Context, int, string, string, domain.MergeStrategy, string, string, string) (domain.MergeResult, error) {
			merged = true
			s.forge.merged = append(s.forge.merged, s.forge.head)
			return domain.MergeResult{SHA: "merge-" + s.forge.head}, nil
		},
	}
	state := NewState(5000, 4, 0, nil, AgentTotals{})
	p := *pending
	state.PendingReactions[ReactionKey(hopExitIssueID, ReactionKindAutoMerge)] = &p
	params := autoMergeParams(&reviewReconcileStore{}, scm, &reviewTrackerStub{})
	params.AutoMergeConfig = s.w.am
	params.NowFunc = func() time.Time { return s.clock.Now().Add(time.Hour) }
	params.ReviewGate = planReviewGate(s.w.cfg, &labelTracker{labels: []string{s.value, s.w.review.Stage}})
	reconcileAutoMerge(state, params, discardLogger(), context.Background(), newAutoMergeMetricsSpy())
	state.TrackerOpsWg.Wait()
	return merged
}

// renderFix renders the first turn of a fix run with Sortie's own choice
// between the template's branch and its built-in prompt. A fix run runs on
// the review rule.
func renderFix(t *testing.T, w stage0Fix, continuation map[string]any) string {
	t.Helper()
	return renderFixTurn(t, w, 1, continuation)
}

// renderFixTurn renders the given turn of a fix run, as renderFix does.
func renderFixTurn(t *testing.T, w stage0Fix, turn int, continuation map[string]any) string {
	t.Helper()
	issue := domain.Issue{
		ID: "ci-41", Identifier: "ONS-41", Title: "Count the stories",
		Description: "## Acceptance criteria\n\n- The workbench counts the stories.",
		Labels:      []string{config.ReviewCriteriaLabel, w.review.Stage},
	}
	text, err := prompt.BuildReactionTurnPrompt(w.tmpl, issue.ToTemplateMap(), 0, turn, w.cfg.Agent.MaxTurns, continuation,
		prompt.WithStage(prompt.StageContext{Current: w.review.Name}))
	if err != nil {
		t.Fatalf("%s: render the fix run: %v", w.name, err)
	}
	return strings.Join(strings.Fields(text), " ")
}

func wantPrompt(t *testing.T, name, text string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("%s: fix prompt lacks %q", name, w)
		}
	}
}

// The kit's fix prompt, whichever reaction started the run.
var fixPromptRules = []string{
	"You are fixing the pull request of story **ONS-41**",
	"The workbench counts the stories.",
	"Read its `AGENTS.md` first",
	"Bring the base in only by a merge, never by a rebase.",
	"Never rewrite a commit that is already pushed",
	"do not amend, rebase, squash, reset, or drop any commit that `origin/<branch>` holds",
	"When the failure comes from a setting the change exposed, such as a CI workflow or toolchain setting, fix that setting on the branch",
	"raised to its latest stable release",
	"Never disable, skip or delete a check or a test",
	"Do not push",
	"Sortie merges the pull request once it passes",
	"check every acceptance criterion above against the branch again",
	"stop with `blocked`",
}

// Both workflows turn on ci_failure and merge_conflicts for every story's
// pull request, on the provider of the other reactions, with no watch
// window, and Sortie's own validation accepts them.
func TestStage0_FixReactionsAreOn(t *testing.T) {
	t.Setenv("STAGE0_GITHUB_TOKEN", "test-token")
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		for _, d := range ValidateReactionConfigs(w.cfg) {
			t.Errorf("%s: %s: %s", name, d.Check, d.Message)
		}
		// Sortie moves reactions.ci_failure into its CI settings.
		for _, kind := range []string{"merge_conflicts", "auto_merge"} {
			if p := w.cfg.Reactions[kind].Provider; p != "github" {
				t.Errorf("%s: reactions.%s.provider = %q, want github", name, kind, p)
			}
		}
		ci := w.cfg.CIFeedback
		if ci.Kind != "github" || ci.MaxRetries < 1 || ci.WatchWindowMS != 0 || ci.MaxLogLines < 1 {
			t.Errorf("%s: ci_failure = %+v, want github, a fix run or more, a log excerpt and no window", name, ci)
		}
		if w.mc.MaxRetries < 1 || w.mc.WatchWindowMS != 0 {
			t.Errorf("%s: merge_conflicts = %+v, want a fix run or more and no window", name, w.mc)
		}
		// Sortie builds the CI provider at start from the github block; it
		// reads the checks of one repository.
		adapter := map[string]any{}
		config.MergeAdapterExtensions(adapter, w.cfg, "github")
		if adapter["project"] != fixRepoProject[name] {
			t.Errorf("%s: github.project = %v, want %s", name, adapter["project"], fixRepoProject[name])
		}
		if _, err := github.NewGitHubCIProvider(ci.MaxLogLines, adapter); err != nil {
			t.Errorf("%s: Sortie cannot build the CI provider: %v", name, err)
		}
		// A stopped failure reaches the story.
		var tracker []domain.EventType
		for _, n := range w.cfg.Notifications.Backends {
			if n.Kind == domain.TrackerCommentKind {
				tracker = n.Events
			}
		}
		for _, e := range []domain.EventType{domain.EventEscalationCIFailure, domain.EventEscalationMergeConflicts} {
			if !slices.Contains(tracker, e) {
				t.Errorf("%s: the tracker_comment entry lacks %s", name, e)
			}
		}
	}
}

// The fix prompt: a fix run merges the base in, never rewrites a pushed
// commit, fixes a setting the change exposed, and is not the review's or
// the build's prompt, nor Sortie's built-in one, which rebases.
func TestStage0_FixPrompt(t *testing.T) {
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		conflict := renderFix(t, w, map[string]any{"merge_conflict": map[string]any{
			"pr_number": 57, "branch": "sortie/ONS-57", "head_sha": "c57", "base": "main",
		}})
		wantPrompt(t, name, conflict, fixPromptRules...)
		wantPrompt(t, name, conflict, "Pull request #57 (`sortie/ONS-57`) conflicts with its base branch `main`",
			"run `git merge origin/main` on the story's branch", "keep both sides' tests", "Commit the merge.")

		failing := domain.CIResult{
			Status: domain.CIStatusFailing, FailingCount: 1, Ref: "c57",
			CheckRuns:  []domain.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure"}},
			LogExcerpt: "[sortie] Failing step: make test\ngo: go.mod requires go >= 1.27.3",
		}
		ci := renderFix(t, w, map[string]any{"ci_failure": failing.ToTemplateMap()})
		wantPrompt(t, name, ci, fixPromptRules...)
		wantPrompt(t, name, ci, "CI fails on `c57`, with 1 failing", "- test: failure",
			`<ci_log>"[sortie] Failing step: make test\ngo: go.mod requires go \u003e= 1.27.3"</ci_log>`,
			"`go.mod`, `go.work`, the CI workflows")

		for _, text := range []string{conflict, ci} {
			for _, never := range []string{"You are reviewing", "You are working on story", "Rebase ", "Push the"} {
				if strings.Contains(text, never) {
					t.Errorf("%s: fix prompt holds %q", name, never)
				}
			}
		}
		if strings.Contains(ci, "conflicts with its base branch") || strings.Contains(conflict, "CI fails on") {
			t.Errorf("%s: a fix run's prompt names the other reaction", name)
		}
	}
}

// A later turn of a fix run does not ask for the fix again. Before, every
// turn sent the whole merge task; the agent, seeing origin/<branch>
// unchanged because Sortie pushes after the run, redid the merge and parked
// (ONS-50, ONS-51, ONS-55, ONS-83).
func TestStage0_FixPromptLaterTurn(t *testing.T) {
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		for _, continuation := range []map[string]any{
			{"merge_conflict": map[string]any{"pr_number": 57, "branch": "sortie/ONS-57", "head_sha": "c57", "base": "main"}},
			{"ci_failure": (&domain.CIResult{Status: domain.CIStatusFailing, FailingCount: 1, Ref: "c57"}).ToTemplateMap()},
		} {
			text := renderFixTurn(t, w, 2, continuation)
			wantPrompt(t, name, text,
				"You are still fixing the pull request of story **ONS-41**",
				"The commits you made in earlier turns of this run are in this workspace.",
				"`origin/<branch>` does not move during the run; that is not a failed push.",
				"Do not repeat a fix you already committed",
				"write no `.sortie/status`", "stop with `blocked`")
			for _, never := range []string{"git merge origin/main", "CI fails on", "You are reviewing", "Rebase "} {
				if strings.Contains(text, never) {
					t.Errorf("%s: turn 2 of a fix run holds %q: %s", name, never, text)
				}
			}
		}
	}
}

// Scenario: Another merge conflicts with an open pull request. #54 merges
// and #57 now conflicts with main: a fix run merges main into #57's branch,
// keeping both sides' tests, and Sortie pushes it; the run ends as a
// review run, CI runs again, and Sortie merges it.
func TestStage0_AnotherMergeConflictsWithAnOpenPullRequest(t *testing.T) {
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
			t.Run(name+"/"+value, func(t *testing.T) {
				s := newFixScenario(t, w, value)
				s.exitRun("")

				// #54 merges: #57 conflicts with main, and cannot merge.
				s.forge.dirty = true
				if s.autoMerge("success") {
					t.Fatal("Sortie merged a conflicting pull request")
				}
				s.checkMergeConflicts()
				text := s.fixRun(ReactionKindMergeConflict)
				wantPrompt(t, name, text, "conflicts with its base branch `main`", "run `git merge origin/main`",
					"keep both sides' tests", "Never rewrite a commit that is already pushed")

				// The fix run merges main in and Sortie pushes the merge.
				s.forge.head, s.forge.dirty = "merge-main-into-57", false
				s.exitRun(ReactionKindMergeConflict)
				s.checkMergeConflicts()
				if r := s.state.RetryAttempts[hopExitIssueID]; r != nil {
					t.Errorf("after the merge: run scheduled %+v, want none", r)
				}
				if n := s.state.ReactionAttempts[ReactionKey(hopExitIssueID, ReactionKindMergeConflict)]; n != 0 {
					t.Errorf("merge-conflict fixes counted = %d after the conflict cleared, want 0", n)
				}

				// CI runs again on the merge; Sortie merges once it passes.
				if s.autoMerge("pending") {
					t.Error("Sortie merged before CI passed on the merge")
				}
				if !s.autoMerge("success") || !slices.Equal(s.forge.merged, []string{"merge-main-into-57"}) {
					t.Errorf("merged heads = %v, want the merge of main", s.forge.merged)
				}
				if len(s.issues.comments) != 0 {
					t.Errorf("comments = %v, want none: nothing stopped", s.issues.comments)
				}
			})
		}
	}
}

// Scenario: CI fails for a setting the change exposed. A newly enabled test
// fails because the workspace's Go is older than a dependency needs: a fix
// run is told to raise it to the latest stable release on the branch, and
// once CI passes on the fix Sortie merges it.
func TestStage0_CIFailsForASettingTheChangeExposed(t *testing.T) {
	failing := domain.CIResult{
		Status: domain.CIStatusFailing, FailingCount: 1,
		CheckRuns: []domain.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure"}},
		LogExcerpt: "[sortie] Failing step: make test\n" +
			"go: github.com/coder/websocket@v1.9.0 requires go >= 1.27.3 (running go 1.26.2; GOTOOLCHAIN=local)",
	}
	passing := domain.CIResult{Status: domain.CIStatusPassing, CheckRuns: []domain.CheckRun{{Name: "test", Status: "completed", Conclusion: "success"}}}
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		for _, value := range []string{reviewHuman, config.ReviewCriteriaLabel} {
			t.Run(name+"/"+value, func(t *testing.T) {
				s := newFixScenario(t, w, value)
				s.exitRun("")

				s.checkCI(failing)
				if s.autoMerge("failure") {
					t.Fatal("Sortie merged a pull request whose CI fails")
				}
				text := s.fixRun(ReactionKindCI)
				wantPrompt(t, name, text, "CI fails on `deadbeef`", `requires go \u003e= 1.27.3 (running go 1.26.2`,
					"a setting the change exposed", "raised to its latest stable release", "`go.mod`, `go.work`, the CI workflows")

				// The fix run raises Go on the branch; CI passes on it.
				s.forge.head = "raise-go"
				s.exitRun(ReactionKindCI)
				s.checkCI(passing)
				if r := s.state.RetryAttempts[hopExitIssueID]; r != nil {
					t.Errorf("after CI passed: run scheduled %+v, want none", r)
				}
				if n := s.state.ReactionAttempts[ReactionKey(hopExitIssueID, ReactionKindCI)]; n != 0 {
					t.Errorf("CI fixes counted = %d after CI passed, want 0", n)
				}
				if !s.autoMerge("success") || !slices.Equal(s.forge.merged, []string{"raise-go"}) {
					t.Errorf("merged heads = %v, want the fix", s.forge.merged)
				}
				if len(s.issues.comments) != 0 {
					t.Errorf("comments = %v, want none: nothing stopped", s.issues.comments)
				}
			})
		}
	}
}

// A pull request that still fails after the reaction's retries is routed
// as a stopped failure: Sortie escalates it, comments on the story, runs no
// further fix and does not merge it, and does so once for that head.
func TestStage0_APullRequestThatStillFailsIsAStoppedFailure(t *testing.T) {
	failing := domain.CIResult{
		Status: domain.CIStatusFailing, FailingCount: 1,
		CheckRuns: []domain.CheckRun{{Name: "test", Status: "completed", Conclusion: "failure"}},
	}
	for _, name := range stage0Workflows {
		w := loadStage0Fix(t, name)
		t.Run(name+"/ci", func(t *testing.T) {
			s := newFixScenario(t, w, config.ReviewCriteriaLabel)
			s.exitRun("")
			for i := range w.cfg.CIFeedback.MaxRetries {
				s.checkCI(failing)
				s.fixRun(ReactionKindCI)
				s.forge.head = "fix-" + string(rune('a'+i))
				s.exitRun(ReactionKindCI)
			}
			s.checkCI(failing)
			assertStoppedFailure(t, s, ReactionKindCI, "CI is still failing")
			if s.autoMerge("failure") {
				t.Error("Sortie merged a pull request whose CI still fails")
			}
		})
		t.Run(name+"/merge-conflict", func(t *testing.T) {
			s := newFixScenario(t, w, config.ReviewCriteriaLabel)
			s.exitRun("")
			s.forge.dirty = true
			for i := range w.mc.MaxRetries {
				s.checkMergeConflicts()
				s.fixRun(ReactionKindMergeConflict)
				s.forge.head = "merge-" + string(rune('a'+i))
				s.exitRun(ReactionKindMergeConflict)
			}
			s.checkMergeConflicts()
			assertStoppedFailure(t, s, ReactionKindMergeConflict, "#41")
			if s.autoMerge("success") {
				t.Error("Sortie merged a pull request that still conflicts")
			}
		})
	}
}

// assertStoppedFailure checks the stopped failure of the kind's watch; its
// comment on the story says what.
func assertStoppedFailure(t *testing.T, s *fixScenario, kind, what string) {
	t.Helper()
	rkey := ReactionKey(hopExitIssueID, kind)
	entry := s.state.PendingReactions[rkey]
	if entry == nil || !entry.escalationStands() || entry.EscalationTrigger != EscalationTriggerBudget {
		t.Fatalf("%s watch = %+v, want its escalation standing, for the spent fixes", kind, entry)
	}
	if r := s.state.RetryAttempts[hopExitIssueID]; r != nil {
		t.Errorf("after the last fix: run scheduled %+v, want none", r)
	}
	if len(s.issues.comments) != 1 || !strings.Contains(s.issues.comments[0], what) {
		t.Errorf("comments = %q, want one on the story saying %q", s.issues.comments, what)
	}
	if len(s.issues.labels) != 0 {
		t.Errorf("labels = %v, want none: the comment and the escalation route it", s.issues.labels)
	}
	// The workbench has it too, under "Needs a person".
	event := map[string]domain.EventType{ReactionKindCI: domain.EventEscalationCIFailure}[kind]
	if event == "" {
		event = domain.EventEscalationMergeConflicts
	}
	if got := stage0Workbench.of(event); len(got) != 1 {
		t.Errorf("the workbench received %d %s, want 1", len(got), event)
	} else {
		wantWorkbenchShape(t, event, got[0])
	}
	// The escalation stands: the next pass neither runs a fix nor posts
	// again.
	switch kind {
	case ReactionKindCI:
		s.checkCI(s.ci.result)
	default:
		s.checkMergeConflicts()
	}
	if r := s.state.RetryAttempts[hopExitIssueID]; r != nil || len(s.issues.comments) != 1 {
		t.Errorf("a pass after the %s escalation: run %+v, comments %q, want none and one", kind, r, s.issues.comments)
	}
}
