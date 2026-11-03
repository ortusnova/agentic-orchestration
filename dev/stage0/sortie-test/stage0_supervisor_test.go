// The stage-0 kit's supervisor (agent-platform/run-supervision#Sortie runs
// a supervisor agent for what its rules cannot settle, and #The supervisor
// asks a person for what needs one), checked against Sortie itself:
// dev/stage0/test-review-gate.sh compiles this file into Sortie's
// orchestrator package. It loads the kit's workflow with Sortie's loader,
// renders the kit's prompt (supervisor.md) with Sortie's supervisor
// template, and runs each scenario through Sortie's supervisor loop with
// the kit's supervisor section and prompt, a fake supervisor agent that
// acts only on what the kit's prompt tells it, and Sortie's test fakes for
// the tracker and the forge.

package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/sortie-ai/sortie/internal/agent/opencode"
	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/parkanswer"
	"github.com/sortie-ai/sortie/internal/persistence"
	"github.com/sortie-ai/sortie/internal/prompt"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/waittest"
	"github.com/sortie-ai/sortie/internal/workflow"
)

// stage0SupervisorPrompt is the kit's prompt file, as stage0.sh installs it.
func stage0SupervisorPrompt(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(os.Getenv("STAGE0_KIT"), "supervisor.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// oneLine folds a text's whitespace, so a phrase matches across line breaks.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// The workflow sets the supervisor section: the host's opencode, the prompt
// the kit installs beside the workflow, a sweep every 30 minutes and a
// daily budget; the supervisor token is its own, read from Sortie's
// environment.
func TestStage0_TheSupervisorRunsOnTheSortie(t *testing.T) {
	// What run-sortie.sh exports from the tokens stage0.sh mints.
	t.Setenv("STAGE0_SORTIE_TOKEN", "control-token")
	t.Setenv("STAGE0_SUPERVISOR_TOKEN", "supervisor-token")
	for _, name := range stage0Workflows {
		mgr := loadStage0Workflow(t, name)
		cfg := mgr.Config()
		sc := cfg.Supervisor
		if !sc.Enabled() || sc.Agent != "opencode" || !registry.Agents.Has(sc.Agent) {
			t.Fatalf("%s: supervisor = %+v, want the opencode agent, a kind Sortie has", name, sc)
		}
		if sc.SweepIntervalMS != int((30 * time.Minute).Milliseconds()) {
			t.Errorf("%s: sweep_interval_ms = %d, want 30 minutes", name, sc.SweepIntervalMS)
		}
		// The sweeps alone take 48 sessions a day; the events need room too.
		if sc.MaxSessionsPerDay <= 48 || sc.MaxTokensPerDay <= 0 {
			t.Errorf("%s: budget = %d sessions, %d tokens a day, want more sessions than the 48 sweeps", name, sc.MaxSessionsPerDay, sc.MaxTokensPerDay)
		}
		if filepath.IsAbs(sc.Prompt) {
			t.Errorf("%s: prompt = %q, want the file beside the workflow", name, sc.Prompt)
		}
		installed, err := os.ReadFile(filepath.Join(filepath.Dir(mgr.WorkflowAbsPath()), sc.Prompt))
		if err != nil || string(installed) != stage0SupervisorPrompt(t) {
			t.Errorf("%s: the prompt beside the workflow is not the kit's supervisor.md: %v", name, err)
		}
		server := cfg.ExtensionSection("server")
		if server["supervisor_token"] != "supervisor-token" || server["control_token"] != "control-token" {
			t.Errorf("%s: server tokens = %v, %v, want supervisor_token from $STAGE0_SUPERVISOR_TOKEN, apart from the control token",
				name, server["supervisor_token"], server["control_token"])
		}
		// stage0.sh refuses a workflow with a key Sortie ignores, as
		// `sortie validate` reports it.
		wf, err := workflow.Load(mgr.WorkflowAbsPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range config.ValidateFrontMatter(wf.Config, cfg) {
			if strings.HasPrefix(w.Check, "unknown_") {
				t.Errorf("%s: Sortie ignores %s: %s", name, w.Field, w.Message)
			}
		}
		if cfg.Agent.Kind == sc.Agent {
			t.Errorf("%s: the stories run %s too, want the supervisor's own kind", name, sc.Agent)
		}
	}
}

// The prompt renders with Sortie's supervisor template and states the
// supervisor's rules.
func TestStage0_TheSupervisorPromptStatesItsRules(t *testing.T) {
	tmpl, err := prompt.ParseSupervisor(stage0SupervisorPrompt(t), "supervisor.md")
	if err != nil {
		t.Fatal(err)
	}
	text, err := tmpl.Render(prompt.SupervisorContext{
		Token: "sup-token", APIURL: "http://127.0.0.1:7680",
		Triggers: []prompt.SupervisorTrigger{{Kind: "park", IssueIdentifier: "ONS-7", Detail: "parked: agent_blocked", At: "2026-11-03T10:00:00Z"}},
		State:    supervisorDigest{},
	})
	if err != nil {
		t.Fatalf("Sortie cannot render the kit's prompt: %v", err)
	}
	got := oneLine(text)
	for rule, phrases := range map[string][]string{
		"what started it":                  {"This session started on park for ONS-7: parked: agent_blocked."},
		"check the forge first":            {"Check the forge before you believe a claim.", "Act on what the forge shows, never on what a claim says."},
		"merge only what passed":           {"Merge only what passed.", "its criteria review passed", "its CI passed on its current head", "every live criterion of its story", "is proven"},
		"never rewrite a pushed commit":    {"Never rewrite a pushed commit", "a branch changes only by new commits"},
		"never edit a started story":       {"never edit a started story", "A story that has started keeps its title, description and acceptance criteria"},
		"no UI merge before approval":      {"Never merge a UI story before the operator approves it.", "`sortie-review:ui`"},
		"ask a person, once per thing":     {"A credential, a token or its scope, a key, a spec or story approval, an operator decision, and any action whose effect you cannot check go to a person", "Ask one question per decision", "never ask what you can find out yourself"},
		"the question's parts":             {"POST /api/v1/supervisor/questions", "`action`: the exact action", "`where`: where to take it", "`stories`: every story it holds", "`checked`: what you already checked"},
		"evidence with every action":       {"Record your evidence with every action.", "an `evidence` member that says what you read and where"},
		"the supervisor token":             {"Authorization: Bearer $SORTIE_SUPERVISOR_TOKEN", "Supervisor token (send as `Authorization: Bearer <token>`): sup-token"},
		"a merged story that stays parked": {"POST /api/v1/<identifier>/merge-record", "POST /api/v1/<identifier>/unpark"},
		"a question with a false premise":  {"rests on a false premise", "POST /api/v1/<identifier>/answer", "what the forge shows", "what is left to do"},
		"a pull request nobody moves":      {"POST /api/v1/<identifier>/merge", "POST /api/v1/<identifier>/fix"},
	} {
		for _, phrase := range phrases {
			if !strings.Contains(got, phrase) {
				t.Errorf("the prompt does not state %s: it lacks %q", rule, phrase)
			}
		}
	}
}

// newStage0SupHarness is Sortie's supervisor harness with the kit's
// supervisor section and prompt; only the agent is the fake supervisor.
func newStage0SupHarness(t *testing.T, mutate func(cfg *config.ServiceConfig)) *supHarness {
	t.Helper()
	kit := loadStage0(t, stage0Workflows[0])
	body := stage0SupervisorPrompt(t)
	return newSupHarness(t, func(cfg *config.ServiceConfig) {
		sc := kit.Supervisor
		sc.Agent = testSupervisorKind
		cfg.Supervisor = sc
		if err := os.WriteFile(filepath.Join(filepath.Dir(cfg.Workspace.Root), sc.Prompt), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if mutate != nil {
			mutate(cfg)
		}
	})
}

// kitSays reports whether the session's prompt holds the kit's prompt with
// each phrase.
func kitSays(prompt string, phrases ...string) bool {
	text := oneLine(prompt)
	if !strings.Contains(text, "You are the supervisor of the stage-0 Sortie") {
		return false
	}
	for _, p := range phrases {
		if !strings.Contains(text, p) {
			return false
		}
	}
	return true
}

// Scenario "A merged story stays parked": ONS-7 is parked and the forge
// shows its pull request merged. At the sweep the supervisor, told by the
// kit's prompt, records the merge and releases the park; the story ends and
// ONS-9, which it blocked, is dispatched.
func TestStage0_AMergedStoryStaysParked(t *testing.T) {
	h := newStage0SupHarness(t, func(cfg *config.ServiceConfig) {
		cfg.Tracker.TerminalStates = []string{"Done"}
		// The kit's 30 minutes, shortened so the test sees a sweep.
		cfg.Supervisor.SweepIntervalMS = 1
	})
	h.params = func(p *OrchestratorParams) {
		p.MergeCompletionConfig = MergeCompletionReactionConfig{TargetState: "Done"}
		p.MergeCompletionReactionConfigured = true
	}
	h.forge.status = domain.PRMergeStatus{Merged: true, Closed: true, MergeCommitSHA: "abc123"}
	h.tracker.transitionIssueFn = func(_ context.Context, id, target string) error {
		if target == "Done" {
			h.tracker.finish(id)
		}
		return nil
	}
	h.tracker.blockers["id-9"] = []string{"id-7"}
	dispatched := make(chan string, 16)
	h.story = &mockAgentAdapter{startSessionFn: func(_ context.Context, params domain.StartSessionParams) (domain.Session, error) {
		dispatched <- filepath.Base(params.WorkspacePath)
		return domain.Session{ID: "story"}, nil
	}}
	var once sync.Once
	results := make(chan []SupervisorResult, 1)
	h.sup.setTurn(func(ctx context.Context, prompt string, _ func(domain.AgentEvent)) (domain.TurnResult, error) {
		if strings.Contains(prompt, "- sweep") && strings.Contains(prompt, `"reason": "agent_blocked"`) &&
			kitSays(prompt, "whose pull request the forge shows merged", "/merge-record", "/unpark") {
			once.Do(func() {
				sup := h.o.Supervisor()
				results <- []SupervisorResult{sup.RecordMerge(ctx, "ONS-7", "o/r#12", "abc123"), sup.ReleasePark(ctx, "ONS-7")}
			})
		}
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
	})
	h.run()
	h.park("id-7", "ONS-7")
	h.tracker.setCandidates(
		domain.Issue{ID: "id-7", Identifier: "ONS-7", Title: "seven", State: "To Do"},
		domain.Issue{ID: "id-9", Identifier: "ONS-9", Title: "nine", State: "To Do"},
	)

	got := waittest.Receive(t, results, "the sweep's actions")
	if got[0].Status != SupervisorDone || got[0].MergeCommit != "abc123" || got[1].Status != SupervisorDone {
		t.Fatalf("actions = %+v, want the merge recorded and the park released", got)
	}
	rows, err := h.store.LoadPlanSatisfaction(t.Context(), []string{"id-7"})
	if err != nil || len(rows["id-7"]) != 1 || rows["id-7"][0].Kind != persistence.PlanSatisfactionMerged {
		t.Errorf("plan satisfaction = %+v, %v, want the merge", rows, err)
	}
	waittest.Until(t, func() error {
		if h.tracker.stateOf("id-7") != "Done" {
			return fmt.Errorf("ONS-7 is %s, want Done", h.tracker.stateOf("id-7"))
		}
		return nil
	})
	h.refresh()
	if got := waittest.Receive(t, dispatched, "the dispatch of ONS-9"); got != "ONS-9" {
		t.Errorf("dispatched %s, want ONS-9", got)
	}
}

// Scenario "A question with a false premise": a story parks asking a
// person to push a commit the forge already holds; the supervisor answers
// with what the forge shows and what is left, and the story continues.
func TestStage0_AQuestionWithAFalsePremise(t *testing.T) {
	h := newStage0SupHarness(t, nil)
	answered := make(chan ControlReply, 1)
	h.sup.setTurn(func(_ context.Context, prompt string, _ func(domain.AgentEvent)) (domain.TurnResult, error) {
		if !strings.Contains(prompt, "Please push the fix commit") ||
			!kitSays(prompt, "Check the forge before you believe a claim.", "rests on a false premise", "/answer") {
			return domain.TurnResult{}, errors.New("the prompt lacks the park or the kit's rule for it")
		}
		answered <- h.o.AnswerFunc()("ONS-7", parkanswer.Answer{
			Text:   "The forge already holds the fix commit head-1 on the branch (https://github.com/o/r/pull/12); what is left is to rerun make test.",
			Author: "supervisor",
		})
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
	})
	h.run()

	h.park("id-7", "ONS-7")
	if reply := waittest.Receive(t, answered, "the supervisor's answer"); reply.Outcome != ControlDone {
		t.Fatalf("answer = %+v, want done", reply)
	}
	h.tracker.commentMu.Lock()
	comments := slices.Clone(h.tracker.commentCalls)
	h.tracker.commentMu.Unlock()
	if len(comments) != 1 || comments[0].IssueID != "id-7" || !strings.Contains(comments[0].Text, "already holds the fix commit") {
		t.Errorf("comments = %+v, want the supervisor's answer on ONS-7", comments)
	}
	h.onLoop(func(context.Context) {
		release := h.o.state.ParkReleases["id-7"]
		if _, parked := h.o.state.Parked["id-7"]; parked || release == nil || release.ReleaseGesture != unparkTriggerAnswered {
			t.Errorf("park = %v release %+v, want released by the answer, so the story continues", parked, release)
		}
	})
}

// Scenario "A pull request nobody is moving": one pull request passed its
// review and CI and has waited 30 minutes on a person to merge it; the
// sweep's supervisor merges it. Another conflicts with no fix run started;
// the supervisor's merge is refused, so it starts the fix run. Each action
// is recorded with its evidence.
func TestStage0_APullRequestNobodyIsMoving(t *testing.T) {
	t.Run("ready to merge", func(t *testing.T) {
		h := newStage0SupHarness(t, func(cfg *config.ServiceConfig) { cfg.Supervisor.SweepIntervalMS = 1 })
		var once sync.Once
		merged := make(chan SupervisorResult, 1)
		h.sup.setTurn(func(ctx context.Context, prompt string, _ func(domain.AgentEvent)) (domain.TurnResult, error) {
			if strings.Contains(prompt, "- sweep") && strings.Contains(prompt, `"pull_request": "owner/repo#41"`) &&
				kitSays(prompt, "Merge only what passed.", "/merge") {
				once.Do(func() { merged <- h.o.Supervisor().Merge(ctx, "id-41-ident") })
			}
			return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
		})
		h.run()
		h.onLoop(func(context.Context) {
			waiting := newReviewPendingEntry("id-41", 41)
			waiting.CreatedAt = time.Now().Add(-31 * time.Minute)
			h.o.state.PendingReactions[ReactionKey("id-41", ReactionKindReview)] = waiting
		})
		got := waittest.Receive(t, merged, "the supervisor's merge")
		if got.Status != SupervisorDone || got.MergeCommit != "merge-1" || len(h.forge.merges) != 1 {
			t.Fatalf("merge = %+v with merges %v, want the pull request merged once", got, h.forge.merges)
		}
		rows, err := h.store.LoadPlanSatisfaction(t.Context(), []string{"id-41"})
		if err != nil || len(rows["id-41"]) != 1 || rows["id-41"][0].Ref != "merge-1" {
			t.Errorf("plan satisfaction = %+v, %v, want the merge recorded", rows, err)
		}
	})
	t.Run("conflicting", func(t *testing.T) {
		h := newStage0SupHarness(t, nil)
		h.forge.status.Mergeability = domain.MergeabilityDirty
		acted := make(chan []SupervisorResult, 1)
		h.sup.setTurn(func(ctx context.Context, prompt string, _ func(domain.AgentEvent)) (domain.TurnResult, error) {
			if !strings.Contains(prompt, "- stalled_reaction id-41-ident") || !kitSays(prompt, "conflicts with its base", "/fix") {
				return domain.TurnResult{}, errors.New("not the stalled reaction, or not the kit's prompt")
			}
			sup := h.o.Supervisor()
			acted <- []SupervisorResult{sup.Merge(ctx, "id-41-ident"),
				sup.FixRun(ctx, "id-41-ident", "owner/repo#41 conflicts with its base; bring the base in by a merge and resolve the conflict.")}
			return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
		})
		h.run()
		h.onLoop(func(context.Context) {
			stalled := newReviewPendingEntry("id-41", 41)
			stalled.CreatedAt = time.Now().Add(-90 * time.Minute)
			h.o.state.PendingReactions[ReactionKey("id-41", ReactionKindReview)] = stalled
			noteStalledReactions(h.o.state, time.Now())
		})
		got := waittest.Receive(t, acted, "the supervisor's actions")
		if got[0].Status != SupervisorPrecondition || got[0].Code != "not_mergeable" || len(h.forge.merges) != 0 {
			t.Errorf("merge = %+v with merges %v, want refused as not mergeable", got[0], h.forge.merges)
		}
		if got[1].Status != SupervisorDone || got[1].PullRequest != "owner/repo#41" {
			t.Fatalf("fix run = %+v, want started on owner/repo#41", got[1])
		}
	})
}

// Scenario "The supervisor never takes a story's slot": with every story
// slot busy, a park starts the supervisor in its own slot, and a story
// dispatches while the supervisor still runs.
func TestStage0_TheSupervisorNeverTakesAStorysSlot(t *testing.T) {
	h := newStage0SupHarness(t, func(cfg *config.ServiceConfig) { cfg.Agent.MaxConcurrentAgents = 1 })
	releaseStory := make(chan struct{})
	h.story = &mockAgentAdapter{
		runTurnFn: func(ctx context.Context, session domain.Session, _ domain.RunTurnParams) (domain.TurnResult, error) {
			select {
			case <-releaseStory:
				return domain.TurnResult{SessionID: session.ID, ExitReason: domain.EventTurnCompleted}, nil
			case <-ctx.Done():
				return domain.TurnResult{SessionID: session.ID, ExitReason: domain.EventTurnCancelled}, ctx.Err()
			}
		},
	}
	releaseSupervisor := make(chan struct{})
	h.sup.setTurn(blockingTurn(releaseSupervisor))
	defer close(releaseSupervisor)
	h.tracker.setCandidates(domain.Issue{ID: "id-a", Identifier: "ONS-A", Title: "a", State: "To Do"})
	h.run()

	running := func() []string {
		snap, err := h.o.SnapshotFunc()()
		if err != nil {
			t.Fatal(err)
		}
		ids := []string{}
		for _, r := range snap.Running {
			ids = append(ids, r.Identifier)
		}
		slices.Sort(ids)
		return ids
	}
	waittest.Until(t, func() error {
		if got := running(); !slices.Equal(got, []string{"ONS-A"}) {
			return fmt.Errorf("running = %v, want ONS-A", got)
		}
		return nil
	})
	h.park("id-b", "ONS-B")
	if prompt := waittest.Receive(t, h.sup.turns, "the supervisor's turn"); !kitSays(prompt) {
		t.Fatalf("the supervisor's prompt is not the kit's:\n%s", prompt)
	}
	if snap := h.snapshot(); snap.Running == nil || snap.Status != SupervisorStatusRunning {
		t.Fatalf("supervisor = %+v, want running", snap)
	}

	h.tracker.finish("id-a")
	h.tracker.setCandidates(domain.Issue{ID: "id-c", Identifier: "ONS-C", Title: "c", State: "To Do"})
	close(releaseStory)
	waittest.Until(t, func() error {
		if got := running(); slices.Contains(got, "ONS-A") {
			return fmt.Errorf("running = %v, want ONS-A ended", got)
		}
		return nil
	})
	h.refresh()
	waittest.Until(t, func() error {
		if got := running(); !slices.Equal(got, []string{"ONS-C"}) {
			return fmt.Errorf("running = %v, want ONS-C in the one story slot", got)
		}
		return nil
	})
	if snap := h.snapshot(); snap.Running == nil {
		t.Errorf("supervisor = %+v, want it still running beside ONS-C", snap)
	}
}

// Scenario "A token scope": pushes fail because the forge token lacks a
// scope; the supervisor raises one question naming the token, the scope,
// the stories it holds and what it checked, and the stories do not retry
// until the check passes.
func TestStage0_ATokenScope(t *testing.T) {
	h := newStage0SupHarness(t, nil)
	question := SupervisorQuestion{
		Action:  `Give the GitHub token Sortie pushes with ($STAGE0_HOME/github-token) the "workflow" scope`,
		Where:   "GitHub, Settings, Developer settings, Personal access tokens",
		Stories: []string{"ONS-49", "ONS-50"},
		Checked: "Both pushes were refused for the workflow scope; gh api -i /user lists repo but not workflow in X-OAuth-Scopes.",
		Cause:   "Pushes that change .github/workflows need the workflow scope.",
	}
	asked := make(chan []SupervisorQuestionResult, 1)
	h.sup.setTurn(func(ctx context.Context, prompt string, _ func(domain.AgentEvent)) (domain.TurnResult, error) {
		if !kitSays(prompt, "a token or its scope", "Ask one question per decision", "/api/v1/supervisor/questions") {
			return domain.TurnResult{}, errors.New("the kit's prompt does not say to ask")
		}
		sup := h.o.Supervisor()
		first := sup.AskPerson(ctx, question)
		again := question
		again.Stories = []string{"ONS-50"}
		asked <- []SupervisorQuestionResult{first, sup.AskPerson(ctx, again)}
		return domain.TurnResult{ExitReason: domain.EventTurnCompleted}, nil
	})
	h.run()

	h.onLoop(func(ctx context.Context) {
		h.o.state.Claimed["id-50"] = struct{}{}
		ScheduleRetry(h.o.state, ScheduleRetryParams{
			IssueID: "id-50", Identifier: "ONS-50", Attempt: 1, DelayMS: time.Hour.Milliseconds(), Error: "push refused",
		}, h.o.onRetryFire)
		parkIssue(h.o.state, parkIssueParams{
			IssueID: "id-49", Identifier: "ONS-49", Reason: parkReasonRepeatedFailure, Store: h.o.store, Metrics: h.o.metrics,
			Logger: discardLogger(), Ctx: ctx,
		})
	})
	got := waittest.Receive(t, asked, "the supervisor's question")
	first, second := got[0], got[1]
	if first.Status != SupervisorDone || first.Existing || !second.Existing || first.Fault.ID != second.Fault.ID {
		t.Fatalf("questions = %+v, want one question, which the second joined", got)
	}
	if f := first.Fault; f.Action == nil || f.Action.Action != question.Action || f.Action.Where != question.Where || f.Detail != question.Checked {
		t.Errorf("fault = %+v, want the action, where and what was checked", f)
	}
	h.onLoop(func(context.Context) {
		for _, id := range []string{"id-49", "id-50"} {
			if h.o.state.FaultHolds[id] != first.Fault.ID {
				t.Errorf("%s is not held by the question", id)
			}
		}
		if _, retrying := h.o.state.RetryAttempts["id-50"]; retrying {
			t.Error("ONS-50 still retries while the question holds it")
		}
	})
	if done := h.o.PlatformFaultDoneFunc()(t.Context(), first.Fault.ID, "operator"); !done.Resolved {
		t.Fatalf("action done = %+v, want the check to pass and release the stories", done)
	}
}
