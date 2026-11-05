// Each host's deploy state and merge-and-prove (agent-platform/
// supervisor-autonomy#A change provable only after deploy is merged,
// proven, or reverted by Sortie, design D7), checked against Sortie itself:
// the file the kit's deploy-state.sh writes is the one Sortie's
// internal/deploystate reads; the kit's workflow names p1's file as
// health.deploy_state and p1's and taichi's as merge_and_prove.hosts; p1
// copies taichi's over SSH (a stubbed worker host here); and a kit story
// goes through Sortie's merge-and-prove on those files, proven in time or
// reverted, with the revert reaching the operator's channel. The supervisor
// merges such a story with merge-and-prove (supervisor.md, rule 2), and the
// review marks its criterion live-proof with the reason after-deploy.
// dev/stage0/test-review-gate.sh compiles this file into Sortie's
// orchestrator package, beside stage0_review_test.go.

package orchestrator

import (
	"encoding/json"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/deploystate"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/route"
	"github.com/sortie-ai/sortie/internal/prompt"
	"github.com/sortie-ai/sortie/internal/registry"
)

// kitHost is one host's STAGE0_HOME, where the kit's deploy-state.sh runs.
type kitHost struct {
	t    *testing.T
	home string
	env  []string
}

// workerHostHome is a worker host's STAGE0_HOME under its own home, as
// deploy-state.sh reaches it over SSH.
const workerHostHome = ".local/share/ortus-nova/stage0"

// newKitHosts is p1's STAGE0_HOME and taichi's, a stubbed worker host that
// p1 reaches with a fake ssh on PATH, which runs the command in taichi's
// home; p1 records taichi as worker-host.sh add does.
func newKitHosts(t *testing.T) (p1, taichi kitHost) {
	t.Helper()
	dir := t.TempDir()
	hosts := filepath.Join(dir, "hosts")
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	ssh := "#!/bin/sh\nwhile [ \"$1\" = -o ]; do shift 2; done\ncd \"" + hosts + "/$1\" || exit 255\nshift\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(ssh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	p1 = kitHost{t: t, home: filepath.Join(dir, "p1")}
	taichi = kitHost{t: t, home: filepath.Join(hosts, "taichi", workerHostHome), env: []string{"STAGE0_PROFILE=worker"}}
	for _, h := range []kitHost{p1, taichi} {
		if err := os.MkdirAll(h.home, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	p1.run(time.Time{}, `deploy_host_add taichi taichi`)
	return p1, taichi
}

// run runs script with deploy-state.sh sourced, in this host's
// STAGE0_HOME at now (the zero time: the clock), and returns its output.
func (h kitHost) run(now time.Time, script string, args ...string) string {
	h.t.Helper()
	cmd := exec.Command("bash", append([]string{"-euo", "pipefail", "-c", `. "$STAGE0_KIT/deploy-state.sh"; ` + script, "kit"}, args...)...)
	cmd.Env = append(append(os.Environ(), h.env...), "STAGE0_HOME="+h.home)
	if !now.IsZero() {
		cmd.Env = append(cmd.Env, "STAGE0_NOW="+now.UTC().Format(time.RFC3339))
	}
	out, err := cmd.Output()
	if err != nil {
		h.t.Fatalf("deploy-state.sh: %s: %v", script, err)
	}
	return string(out)
}

// write writes this host's deploy state as deploy.sh does.
func (h kitHost) write(now time.Time, status, commit, main, why string) {
	h.t.Helper()
	h.run(now, `deploy_state_write "$@"`, status, commit, main, why)
}

func (h kitHost) file() string { return filepath.Join(h.home, "deploy-state.json") }

func (h kitHost) read() deploystate.State {
	h.t.Helper()
	st, err := deploystate.Read(h.file())
	if err != nil {
		h.t.Fatalf("Sortie cannot read the kit's deploy state: %v", err)
	}
	return st
}

// loadKitWorkflow loads the kit's workflow as stage0.sh renders it on p1,
// whose STAGE0_HOME is p1's, with merge_and_prove.hosts from the kit's
// deploy_hosts_yaml and the operator's channel of that kind.
func loadKitWorkflow(t *testing.T, p1 kitHost, channel string) config.ServiceConfig {
	t.Helper()
	saved := map[string]string{}
	for k, v := range map[string]string{
		"STAGE0_HOME":         p1.home,
		"STAGE0_DEPLOY_HOSTS": strings.TrimRight(p1.run(time.Time{}, "deploy_hosts_yaml"), "\n"),
	} {
		saved[k] = stage0Values[k]
		stage0Values[k] = v
	}
	defer func() {
		for k, v := range saved {
			if v == "" {
				delete(stage0Values, k)
			} else {
				stage0Values[k] = v
			}
		}
	}()
	if channel == "" {
		return loadStage0(t, "WORKFLOW.md")
	}
	return loadWithChannel(t, "WORKFLOW.md", channel, "", "https://hooks.slack.test/T0/B0/channel-secret")
}

// The file the kit writes is the one Sortie reads: an applied deploy is
// healthy, a failed one is a problem with its error, and a commit left
// behind main for more than 30 minutes is one too.
func TestStage0_SortieReadsTheKitsDeployState(t *testing.T) {
	p1, _ := newKitHosts(t)
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

	p1.write(t0, "succeeded", "c0ffee1", "c0ffee1", "")
	st := p1.read()
	if st.Status != deploystate.StatusSucceeded || st.Commit != "c0ffee1" || st.Main != "c0ffee1" || st.BehindSince != nil ||
		st.Error != "" || !st.UpdatedAt.Equal(t0) {
		t.Errorf("applied = %+v, want succeeded at c0ffee1, not behind, written at t0", st)
	}
	if p := st.Problem(t0.Add(time.Minute)); p != "" {
		t.Errorf("an applied deploy is a problem: %s", p)
	}

	p1.write(t0, "failed", "c0ffee1", "beef002", "stage0.sh failed at beef002: \"ortus-stage0-egress-gate\"\tdid not\nrestart")
	st = p1.read()
	if st.Status != deploystate.StatusFailed || st.Commit != "c0ffee1" || st.Main != "beef002" ||
		st.Error != `stage0.sh failed at beef002: "ortus-stage0-egress-gate" did not restart` {
		t.Errorf("failed = %+v, want failed on c0ffee1 with its error on one line", st)
	}
	if p := st.Problem(t0.Add(time.Minute)); !strings.Contains(p, "the last deploy failed: stage0.sh failed at beef002") {
		t.Errorf("a failed deploy's problem = %q, want its error", p)
	}

	// Behind: the same commit stays behind main from t0; the kit keeps
	// when it first fell behind.
	p1.write(t0, "succeeded", "c0ffee1", "beef002", "")
	p1.write(t0.Add(40*time.Minute), "succeeded", "c0ffee1", "beef002", "")
	st = p1.read()
	if st.BehindSince == nil || !st.BehindSince.Equal(t0) || !st.UpdatedAt.Equal(t0.Add(40*time.Minute)) {
		t.Fatalf("behind = %+v, want behind since t0, written at t0+40m", st)
	}
	if p := st.Problem(t0.Add(40 * time.Minute)); !strings.Contains(p, "has been behind main") {
		t.Errorf("a commit behind main for 40 minutes: problem %q, want behind", p)
	}
	if p := st.Problem(t0.Add(2 * time.Hour)); !strings.Contains(p, "has not written its state") {
		t.Errorf("a state an hour old: problem %q, want stale", p)
	}
	p1.write(t0.Add(41*time.Minute), "succeeded", "beef002", "beef002", "")
	if st = p1.read(); st.BehindSince != nil {
		t.Errorf("deployed at main = %+v, want behind_since null", st)
	}
	// A tick with nothing to deploy keeps the status and the error.
	p1.write(t0, "failed", "c0ffee1", "beef002", "the frontend build failed")
	p1.run(t0.Add(time.Minute), `deploy_state_refresh "$@"`, "c0ffee1", "beef003")
	if st = p1.read(); st.Status != deploystate.StatusFailed || st.Error != "the frontend build failed" || st.Main != "beef003" ||
		!st.UpdatedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("refreshed = %+v, want still failed with its error, main beef003", st)
	}
}

// The workflow names p1's file as health.deploy_state and every host's,
// taichi's copy on p1 included, as merge_and_prove.hosts; p1 copies
// taichi's over SSH, so Sortie on p1 reads both hosts.
func TestStage0_P1ReadsEachHostsDeployState(t *testing.T) {
	p1, taichi := newKitHosts(t)
	cfg := loadKitWorkflow(t, p1, "")
	if cfg.Health.DeployState != p1.file() {
		t.Errorf("health.deploy_state = %q, want p1's %q", cfg.Health.DeployState, p1.file())
	}
	copyOnP1 := filepath.Join(p1.home, "hosts", "taichi", "deploy-state.json")
	want := []config.DeployHost{{Name: "p1", DeployState: p1.file()}, {Name: "taichi", DeployState: copyOnP1}}
	if got := cfg.DeployHosts("p1"); !slices.Equal(got, want) {
		t.Errorf("merge_and_prove.hosts = %+v, want %+v", got, want)
	}
	if cfg.MergeAndProve.Deadline != 30*time.Minute {
		t.Errorf("merge_and_prove.deadline = %s, want 30m", cfg.MergeAndProve.Deadline)
	}

	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	taichi.write(now, "succeeded", "beef002", "beef002", "")
	p1.run(time.Time{}, "deploy_state_pull")
	st, err := deploystate.Read(copyOnP1)
	if err != nil || st.Status != deploystate.StatusSucceeded || st.Commit != "beef002" {
		t.Fatalf("taichi's copy on p1 = %+v, %v, want taichi's succeeded at beef002", st, err)
	}
	// A worker host that cannot be reached keeps its last copy, which goes
	// stale for Sortie an hour later.
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(taichi.home))))); err != nil {
		t.Fatal(err)
	}
	p1.run(time.Time{}, "deploy_state_pull 2> /dev/null")
	if st, err = deploystate.Read(copyOnP1); err != nil || st.Commit != "beef002" {
		t.Errorf("after a failed copy = %+v, %v, want the last copy", st, err)
	}
	p1.run(time.Time{}, "deploy_host_remove taichi")
	if got := strings.TrimSpace(p1.run(time.Time{}, "deploy_hosts_yaml")); got != "p1: "+p1.file() {
		t.Errorf("hosts after worker-host.sh remove = %q, want p1 only", got)
	}
}

// kitRepository is one member of the deploy state's repositories (design
// D13), which this Sortie does not read yet.
type kitRepository struct {
	Branch string `json:"branch"`
	Commit string `json:"commit"`
}

// kitRepositories reads the repositories the deploy state at path lists.
func kitRepositories(t *testing.T, path string) map[string]kitRepository {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // the test's own file
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Repositories map[string]kitRepository `json:"repositories"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("the deploy state at %s is not JSON: %v", path, err)
	}
	return f.Repositories
}

// Each host's deploy state lists every repository the host deploys: p1
// the kit at the main commit it runs and Sortie at the plan-bench commit
// of the Sortie it runs, taichi, which runs no Sortie, the kit only; p1's
// copy of taichi's keeps the list. Sortie's internal/deploystate still
// reads each file, with status, commit and main as before.
func TestStage0_EachHostsDeployStateListsItsRepositories(t *testing.T) {
	p1, taichi := newKitHosts(t)
	now := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	const sortieRan = "5071e0000000000000000000000000000000beef"
	p1.run(time.Time{}, `deploy_sortie_ran "$1"`, sortieRan)
	// A record its retired Sortie left: taichi lists no Sortie all the same.
	taichi.run(time.Time{}, `deploy_sortie_ran "$1"`, sortieRan)

	p1.write(now, "succeeded", "beef002", "beef002", "")
	taichi.write(now, "failed", "beef001", "beef002", "stage0.sh failed at beef002")
	p1.run(time.Time{}, "deploy_state_pull")
	copyOnP1 := filepath.Join(p1.home, "hosts", "taichi", "deploy-state.json")

	if st := p1.read(); st.Status != deploystate.StatusSucceeded || st.Commit != "beef002" || st.Main != "beef002" || !st.UpdatedAt.Equal(now) {
		t.Errorf("p1's = %+v, want succeeded at beef002, written at now", st)
	}
	for _, path := range []string{taichi.file(), copyOnP1} {
		st, err := deploystate.Read(path)
		if err != nil || st.Status != deploystate.StatusFailed || st.Commit != "beef001" || st.Main != "beef002" ||
			st.Error != "stage0.sh failed at beef002" {
			t.Errorf("taichi's at %s = %+v, %v, want failed at beef001 behind beef002", path, st, err)
		}
	}

	kit := kitRepository{Branch: "main", Commit: "beef002"}
	want := map[string]kitRepository{
		"ortusnova/agentic-orchestration": kit,
		"ortusnova/sortie":                {Branch: "plan-bench", Commit: sortieRan},
	}
	if got := kitRepositories(t, p1.file()); !maps.Equal(got, want) {
		t.Errorf("p1's repositories = %+v, want %+v", got, want)
	}
	want = map[string]kitRepository{"ortusnova/agentic-orchestration": {Branch: "main", Commit: "beef001"}}
	for _, path := range []string{taichi.file(), copyOnP1} {
		if got := kitRepositories(t, path); !maps.Equal(got, want) {
			t.Errorf("taichi's repositories at %s = %+v, want %+v", path, got, want)
		}
	}
}

// kitProve is Sortie's merge-and-prove fixture on the kit's workflow: its
// hosts are p1's and taichi's files as the kit writes and copies them, and
// its notifications the kit's, with the operator's channel (slack).
type kitProve struct {
	*proveFixture
	p1, taichi kitHost
	channel    map[string]*notifierSpy
}

func newKitProve(t *testing.T) *kitProve {
	t.Helper()
	f := newProveFixture(t)
	p1, taichi := newKitHosts(t)
	kit := loadKitWorkflow(t, p1, "slack")
	cfg := f.o.workflowManager.Config()
	cfg.MergeAndProve = kit.MergeAndProve
	cfg.Health.DeployState = kit.Health.DeployState
	f.o.workflowManager = &stubWorkflowManager{config: cfg}
	spies := map[string]*notifierSpy{"slack": {}, "webhook": {}}
	lookup := func(kind string) (registry.NotifierConstructor, error) {
		spy := spies[kind]
		return func(map[string]any) (domain.Notifier, error) { return spy, nil }, nil
	}
	var entries []config.NotificationBackend
	for _, b := range kit.Notifications.Backends {
		if b.Kind != "tracker_comment" {
			entries = append(entries, b)
		}
	}
	f.o.router = mustRouter(t, nil, lookup, route.Inputs{Entries: entries})
	k := &kitProve{proveFixture: f, p1: p1, taichi: taichi, channel: spies}
	k.deployBoth(f.t0, "old-main", false)
	return k
}

// deployBoth has each host's kit write that it runs commit; p1 copies
// taichi's when copied.
func (k *kitProve) deployBoth(now time.Time, commit string, copied bool) {
	k.p1.write(now, "succeeded", commit, commit, "")
	k.taichi.write(now, "succeeded", commit, commit, "")
	if copied {
		k.p1.run(time.Time{}, "deploy_state_pull")
	}
}

// Scenario "Proven in time", on the kit: a kit story merges through the
// route, p1 and taichi deploy it, each writing its deploy state, p1
// copying taichi's, and its live proof is proven 12 minutes later. The
// story is Done, and the next queued story's merge starts.
func TestStage0_AnAfterDeployStoryProvenInTime(t *testing.T) {
	k := newKitProve(t)
	k.p1.run(time.Time{}, "deploy_state_pull")
	k.accept(t, "ONS-1")
	k.accept(t, "ONS-2")
	k.tick(t, k.t0)
	if got := k.forge.mergedPulls(); !slices.Equal(got, []int{1}) {
		t.Fatalf("merged = %v, want o/kit#1 only", got)
	}

	k.forge.holds("new-main")
	k.deployBoth(k.t0.Add(2*time.Minute), "new-main", false)
	k.tick(t, k.t0.Add(3*time.Minute))
	if in := k.snapshot(t).InFlight; in.Phase != MergeAndProveDeploying || !strings.Contains(in.Waiting, "taichi deployed old-main") {
		t.Fatalf("before p1 copies taichi's state = %+v, want deploying, waiting on taichi", in)
	}
	k.p1.run(time.Time{}, "deploy_state_pull")
	deployed := k.t0.Add(4 * time.Minute)
	k.tick(t, deployed)
	in := k.snapshot(t).InFlight
	if in.Phase != MergeAndProveProving || !slices.Equal(in.Deployed, []DeployedHost{{"p1", "new-main"}, {"taichi", "new-main"}}) {
		t.Fatalf("once both hosts deployed = %+v, want proving on p1 and taichi at new-main", in)
	}
	onLoop(t, k.o, func() { k.o.state.RetryAttempts = map[string]*RetryEntry{} }) // the job ran

	k.forge.comment(1, "c-1", "`"+LiveProofMarker+" "+mergeSHA(1)+"`")
	k.tick(t, deployed.Add(12*time.Minute))
	if got := k.stories.stateOf("id-1"); got != "Done" {
		t.Errorf("ONS-1 = %q, want Done", got)
	}
	if got := k.forge.mergedPulls(); !slices.Equal(got, []int{1, 2}) {
		t.Errorf("merged = %v, want ONS-2's merge started", got)
	}
}

// Scenario "Not proven", on the kit: the deadline passes with no proven
// record. Sortie merges a revert pull request, the story returns to Todo
// with the reason, and the operator's channel receives the event.
func TestStage0_AnAfterDeployStoryNotProvenIsReverted(t *testing.T) {
	k := newKitProve(t)
	k.accept(t, "ONS-1")
	k.tick(t, k.t0)
	k.forge.holds("new-main")
	k.deployBoth(k.t0.Add(2*time.Minute), "new-main", true)
	deployed := k.t0.Add(3 * time.Minute)
	k.tick(t, deployed)
	if in := k.snapshot(t).InFlight; in.Phase != MergeAndProveProving {
		t.Fatalf("after the deploy = %+v, want proving", in)
	}
	k.tick(t, deployed.Add(30*time.Minute))
	if in := k.snapshot(t).InFlight; in.Phase != MergeAndProveReverting {
		t.Fatalf("at the deadline = %+v, want reverting", in)
	}
	k.forge.setCI(101, "success")
	k.tick(t, deployed.Add(32*time.Minute))
	if got := k.forge.mergedPulls(); !slices.Equal(got, []int{1, 101}) {
		t.Fatalf("merged = %v, want the story, then its revert", got)
	}
	if got := k.stories.stateOf("id-1"); got != "Todo" {
		t.Errorf("ONS-1 = %q, want Todo", got)
	}
	if c := k.stories.lastComment("id-1"); !strings.Contains(c, "no comment on o/kit#1 recorded the live proof proven") {
		t.Errorf("ONS-1's comment = %q, want the unproven result as its reason", c)
	}
	for kind, spy := range k.channel {
		sent := spy.notifications()
		if !slices.ContainsFunc(sent, func(n domain.Notification) bool {
			return n.Envelope.EventType == domain.EventStoryReverted && n.Envelope.Identifier == "ONS-1"
		}) {
			t.Errorf("the %s entry got %+v, want story.reverted for ONS-1", kind, sent)
		}
	}
}

// Rule 2 of the kit's supervisor prompt: an after-deploy story is merged
// with merge-and-prove, never with merge.
func TestStage0_TheSupervisorMergesAnAfterDeployStoryWithMergeAndProve(t *testing.T) {
	tmpl, err := prompt.ParseSupervisor(stage0SupervisorPrompt(t), "supervisor.md")
	if err != nil {
		t.Fatal(err)
	}
	text, err := tmpl.Render(prompt.SupervisorContext{Token: "t", APIURL: "http://127.0.0.1:7680",
		Triggers: []prompt.SupervisorTrigger{{Kind: "sweep", Detail: "sweep", At: "2026-11-05T10:00:00Z"}}, State: supervisorDigest{}})
	if err != nil {
		t.Fatalf("Sortie cannot render the kit's prompt: %v", err)
	}
	got := oneLine(text)
	rule2 := regexp.MustCompile(`2\. \*\*Merge only what passed\.\*\*.*?3\. \*\*`).FindString(got)
	for _, want := range []string{
		"A pull request whose only unmet criteria are after-deploy live criteria is merged with merge-and-prove once its other criteria are met and CI passes, never with merge; Sortie proves or reverts it.",
		"`live-proof` with the reason `after-deploy`",
	} {
		if !strings.Contains(rule2, want) {
			t.Errorf("rule 2 lacks %q: %s", want, rule2)
		}
	}
	if !strings.Contains(got, "`POST /api/v1/<identifier>/merge-and-prove` instead, never with `merge`") {
		t.Error("the actions do not name the merge-and-prove route")
	}
}

// The review prompt marks a criterion only the deployed main settles
// live-proof with the reason after-deploy, and its example entry is one
// Sortie takes for merge-and-prove.
func TestStage0_TheReviewMarksAnAfterDeployCriterion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("STAGE0_KIT"), "WORKFLOW.md"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	start := strings.Index(body, `{{- else if eq .stage.current "review" -}}`)
	end := strings.Index(body, "{{- else -}}")
	text := strings.Join(strings.Fields(body[start:end]), " ")
	for _, want := range []string{
		"A live criterion that only the deployed main can settle, after the merge",
		"is an after-deploy criterion",
		"Record it `live-proof` with the reason `after-deploy` (`\"reason\": \"after-deploy\"` in its entry)",
		"The supervisor merges such a pull request with merge-and-prove once every other criterion is `met` and CI passes",
		"Give an after-deploy criterion the `reason` `after-deploy`.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("review prompt lacks %q", want)
		}
	}
	example := regexp.MustCompile(`\{"criteria": \[.*?\]\}`).FindString(text)
	example = strings.NewReplacer("<the file and line, the test, or the command you ran and its result>", "make test passed").Replace(example)
	v := evaluateCriteria([]byte(example))
	left, _, ok := v.afterDeployCriteria()
	if v.Outcome != CriteriaOutcomeLiveProof || !ok || !strings.Contains(left, "a criterion only the deployed main settles") {
		t.Errorf("the prompt's example %s: outcome %s, after deploy %q %v, want merge-and-prove's", example, v.Outcome, left, ok)
	}
}

// The live-proof job merge-and-prove schedules after the deploy gets the
// kit's after-deploy prompt: it proves the request's criteria against the
// deployed hosts, commits nothing, and records its result at the merge
// commit, the line Sortie's merge-and-prove reads, never at the branch's
// head.
func TestStage0_TheAfterDeployProofRecordsTheMergeCommit(t *testing.T) {
	w := loadStage0Fix(t, "WORKFLOW.md")
	merge := mergeSHA(41)
	e := &MergeAndProveEntry{
		Identifier: "ONS-41", PullRequest: pullRequestRef{Number: 41, Owner: "ortusnova", Repo: "agentic-orchestration", Branch: "sortie/ONS-41"},
		MergeCommit: merge, Deployed: []DeployedHost{{"p1", merge}, {"taichi", merge}},
		Deadline: time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC), Criteria: "- Live: GET /api/v1/state on p1 shows both hosts\n",
	}
	text := renderFix(t, w, map[string]any{reviewCommentsKey: buildReviewTemplateMap([]domain.ReviewComment{{
		ID: "merge-and-prove-1", Reviewer: "sortie", Body: afterDeployProofRequest(e),
	}})})
	wantPrompt(t, w.name, text,
		"Sortie's merge-and-prove asks for a live proof after the deploy.",
		"Prove each criterion the request lists against the hosts as deployed, not against this workspace",
		"Make no commit: the pull request is merged",
		"a line `Commit: ` with the merge commit the request names",
		"a line `sortie:live-proof proven ` followed by that merge commit",
		"a line `sortie:live-proof unproven ` followed by that merge commit and what failed",
		"Merge commit: "+merge, "p1 at "+merge+", taichi at "+merge)
	for _, never := range []string{"`git rev-parse HEAD`", "The supervisor asks for a live proof.", "fix it with new commits on top of the branch"} {
		if strings.Contains(text, never) {
			t.Errorf("the after-deploy proof's prompt holds %q", never)
		}
	}
	// A supervisor's live-proof request still gets the proof on the head.
	wantPrompt(t, w.name, renderFix(t, w, map[string]any{reviewCommentsKey: buildReviewTemplateMap([]domain.ReviewComment{{
		ID: "supervisor-1", Reviewer: "supervisor", Body: liveProofMessage("ONS-41"),
	}})}), liveProofPromptRules...)
}
