// Where the kit's notifications go (agent-platform/run-supervision#A person
// is alerted, wherever the question is held), checked against Sortie itself:
// the workbench, by Sortie's webhook backend, by default, and the operator's
// channel as one more entry. Each of the kit's workflows, rendered with the
// channel entry that notify-channel.sh writes for each kind, loads with
// Sortie's own loader, and a story that stops for a person reaches the
// workbench and the channel through Sortie's own router and notifier, with
// its question. The body the workbench receives has the fields of the
// workbench's own fixtures of Sortie's payload
// (workbench/tests/fixtures/sortie-webhook), so those tests hold Sortie's shape. dev/stage0/test-review-gate.sh
// compiles this file into Sortie's orchestrator package, beside
// stage0_review_test.go.

package orchestrator

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/sortie-ai/sortie/internal/config"
	"github.com/sortie-ai/sortie/internal/domain"
	"github.com/sortie-ai/sortie/internal/notify/route"
	"github.com/sortie-ai/sortie/internal/registry"
	"github.com/sortie-ai/sortie/internal/workspace"

	_ "github.com/sortie-ai/sortie/internal/notify/discord"
	_ "github.com/sortie-ai/sortie/internal/notify/slack"
	_ "github.com/sortie-ai/sortie/internal/notify/telegram"
	_ "github.com/sortie-ai/sortie/internal/notify/webhook"
)

// notifyEntry is the workflow's notifications entry for a channel kind, as
// stage0.sh renders it.
func notifyEntry(t *testing.T, kind, chat string) string {
	t.Helper()
	out, err := exec.Command(filepath.Join(os.Getenv("STAGE0_KIT"), "notify-channel.sh"), "entry", kind, chat).Output()
	if err != nil {
		t.Fatalf("notify-channel.sh entry %s: %v", kind, err)
	}
	return strings.TrimRight(string(out), "\n")
}

// loadWithChannel loads the workflow with the operator's channel of that
// kind, its credential in Sortie's environment as run-sortie.sh puts it.
func loadWithChannel(t *testing.T, name, kind, chat, secret string) config.ServiceConfig {
	t.Helper()
	t.Setenv("SORTIE_NOTIFY_SECRET", secret)
	saved := stage0Values["STAGE0_NOTIFY_ENTRY"]
	stage0Values["STAGE0_NOTIFY_ENTRY"] = notifyEntry(t, kind, chat)
	defer func() { stage0Values["STAGE0_NOTIFY_ENTRY"] = saved }()
	return loadStage0(t, name)
}

// channelBackend is the workflow's entry of that kind for the operator's
// channel: the last one, after the workbench's.
func channelBackend(t *testing.T, cfg config.ServiceConfig, kind string) config.NotificationBackend {
	t.Helper()
	for _, b := range slices.Backward(cfg.Notifications.Backends) {
		if b.Kind == kind {
			return b
		}
	}
	t.Fatalf("no %s entry in %+v", kind, cfg.Notifications.Backends)
	return config.NotificationBackend{}
}

// fakeChannel records every body posted to it.
type fakeChannel struct {
	mu     sync.Mutex
	bodies []map[string]any
}

func (c *fakeChannel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	c.mu.Lock()
	c.bodies = append(c.bodies, body)
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// texts is what each push says, whatever the kind calls its text.
func (c *fakeChannel) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range c.bodies {
		var parts []string
		for _, k := range []string{"text", "content", "title", "body", "agent_text"} {
			if s, ok := b[k].(string); ok {
				parts = append(parts, s)
			}
		}
		out = append(out, strings.Join(parts, "\n"))
	}
	return out
}

// The workflow sends each question for a person to the operator's channel:
// every kind loads, with the credential from Sortie's environment, and lists
// the events of a person's question.
func TestStage0_EveryKindOfChannelLoads(t *testing.T) {
	for _, name := range stage0Workflows {
		cfg := loadStage0(t, name)
		if b := cfg.Notifications.Backends; len(b) != 2 || b[0].Kind != domain.TrackerCommentKind || b[1].Kind != "webhook" {
			t.Errorf("%s with no channel: notifications = %+v, want the tracker comments and the workbench", name, b)
		}
		for _, kind := range []string{"slack", "discord", "telegram", "webhook"} {
			const secret = "https://channel.test/hook-secret-0123456789"
			cfg := loadWithChannel(t, name, kind, "-1001234567890", secret)
			b := channelBackend(t, cfg, kind)
			for _, e := range []domain.EventType{domain.EventSessionStopped, domain.EventSessionEscalated,
				domain.EventEscalationCIFailure, domain.EventEscalationMergeConflicts} {
				if !slices.Contains(b.Events, e) {
					t.Errorf("%s/%s: events = %v, want %s", name, kind, b.Events, e)
				}
			}
			field := map[string]string{"slack": "webhook_url", "discord": "webhook_url", "telegram": "bot_token", "webhook": "url"}[kind]
			if b.Config[field] != secret {
				t.Errorf("%s/%s: %s = %v, want the credential from SORTIE_NOTIFY_SECRET", name, kind, field, b.Config[field])
			}
			if err := route.CheckDestinations(cfg.Notifications.Backends, registry.Notifiers.Get); err != nil {
				t.Errorf("%s/%s: Sortie cannot build the channel: %v", name, kind, err)
			}
		}
	}
}

// Scenario: A question, wherever its story runs. A story that stops
// blocked with a question, the agent's park in its build or the review's
// question, reaches the operator's channel through Sortie's notifications,
// with the question and the story.
func TestStage0_AQuestionReachesTheOperatorsChannel(t *testing.T) {
	const question = "Which cadence did the operator choose for the Authentik upgrade gate?"
	for _, name := range stage0Workflows {
		for _, kind := range []string{"slack", "discord", "webhook"} {
			for _, rule := range []string{"build", "review"} {
				t.Run(name+"/"+kind+"/"+rule, func(t *testing.T) {
					channel := &fakeChannel{}
					srv := httptest.NewServer(channel)
					defer srv.Close()
					cfg := loadWithChannel(t, name, kind, "", srv.URL+"/hook")
					review := stage0Review(t, cfg)
					f := reviewExit(t, cfg, review, []string{reviewHuman})
					entry := f.state.Running[hopExitIssueID]
					if rule == "build" {
						entry.RuleName = rule
						entry.Issue.Labels = []string{reviewHuman}
						entry.DispatchLabels = slices.Clone(entry.Issue.Labels)
					}
					story := cmp.Or(entry.Issue.DisplayID, entry.Identifier)
					router := route.NewRouter(f.tracker, registry.Notifiers.Get)
					if err := router.Update(route.Inputs{Entries: cfg.Notifications.Backends}); err != nil {
						t.Fatal(err)
					}
					f.params.Router = router
					f.result.SoftStop = true
					f.result.SoftStopReason = string(workspace.StatusBlocked)
					f.result.SoftStopStatement = workspace.StatusStatement{Text: question}
					stage0Workbench.reset()
					f.exit()

					// A review that stops also reports its failed criteria check.
					texts := channel.texts()
					asked := 0
					for _, text := range texts {
						if strings.Contains(text, question) && strings.Contains(text, story) {
							asked++
						}
					}
					if asked != 1 || (rule == "build" && len(texts) != 1) {
						t.Errorf("the channel received %q, want the question and %s once", texts, story)
					}
				})
			}
		}
	}
}

// fakeWorkbench is the workbench's notifications endpoint: every body
// Sortie's webhook backend posts to it, and the path it posts to.
type fakeWorkbench struct {
	once  sync.Once
	srv   *httptest.Server
	mu    sync.Mutex
	posts []workbenchPost
}

type workbenchPost struct {
	path string
	raw  []byte
	body map[string]any
}

// stage0Workbench is the one every loaded workflow posts to.
var stage0Workbench = &fakeWorkbench{}

// The path of p1's notifications, with the token stage0.sh mints.
const workbenchPath = "/api/notifications/p1/p1-notify-token-0123456789"

func (w *fakeWorkbench) url() string {
	w.once.Do(func() {
		w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			w.mu.Lock()
			w.posts = append(w.posts, workbenchPost{path: r.URL.Path, raw: raw, body: body})
			w.mu.Unlock()
			rw.WriteHeader(http.StatusAccepted)
		}))
	})
	return w.srv.URL + workbenchPath
}

func (w *fakeWorkbench) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.posts = nil
}

func (w *fakeWorkbench) received() []workbenchPost {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.posts)
}

// of is what the workbench received of one event type.
func (w *fakeWorkbench) of(event domain.EventType) []workbenchPost {
	var out []workbenchPost
	for _, p := range w.received() {
		if p.body["event_type"] == string(event) {
			out = append(out, p)
		}
	}
	return out
}

// workbenchFixture is one of the workbench's fixtures of Sortie's webhook
// payload, by event type.
func workbenchFixture(t *testing.T, event domain.EventType) (string, map[string]any) {
	t.Helper()
	path := filepath.Join(os.Getenv("STAGE0_KIT"), "..", "..", "workbench", "tests", "fixtures", "sortie-webhook",
		strings.ReplaceAll(string(event), ".", "_")+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return path, body
}

// shape is the fields of a body, and of each object in it.
func shape(body map[string]any) []string {
	var out []string
	for k, v := range body {
		out = append(out, k)
		if obj, ok := v.(map[string]any); ok {
			for kk := range obj {
				out = append(out, k+"."+kk)
			}
		}
	}
	slices.Sort(out)
	return out
}

// wantWorkbenchShape fails unless what Sortie posted has the fields of the
// workbench's fixture of that event.
func wantWorkbenchShape(t *testing.T, event domain.EventType, got workbenchPost) {
	t.Helper()
	writeWorkbenchFixtures(t, []workbenchPost{got})
	path, fixture := workbenchFixture(t, event)
	if !slices.Equal(shape(got.body), shape(fixture)) {
		t.Errorf("Sortie posted the workbench %s with the fields %v; %s has %v", event, shape(got.body), path, shape(fixture))
	}
	if got.path != workbenchPath {
		t.Errorf("Sortie posted to %s, want %s: the host and its token", got.path, workbenchPath)
	}
}

// The workbench is every workflow's default destination: its entry is
// Sortie's webhook backend, at the URL run-sortie.sh puts in Sortie's
// environment, for each event that needs a person, whatever channel the
// operator adds.
func TestStage0_TheWorkbenchIsTheDefaultDestination(t *testing.T) {
	want := []domain.EventType{domain.EventSessionStopped, domain.EventSessionFailed, domain.EventSessionEscalated,
		domain.EventEscalationCIFailure, domain.EventEscalationReviewComments, domain.EventEscalationBotReview,
		domain.EventEscalationMergeConflicts, domain.EventEscalationAutoMerge, domain.EventEscalationMergeCompletion,
		domain.EventBudgetHeld}
	for _, name := range stage0Workflows {
		for _, kind := range []string{"", "slack", "webhook"} {
			cfg := loadStage0(t, name)
			if kind != "" {
				cfg = loadWithChannel(t, name, kind, "", "https://channel.test/hook-secret-0123456789")
			}
			var wb []config.NotificationBackend
			for _, b := range cfg.Notifications.Backends {
				if b.Kind == "webhook" && b.Config["url"] == stage0Workbench.url() {
					wb = append(wb, b)
				}
			}
			if len(wb) != 1 {
				t.Fatalf("%s with channel %q: notifications = %+v, want one entry to the workbench", name, kind, cfg.Notifications.Backends)
			}
			if got := slices.Sorted(slices.Values(wb[0].Events)); !slices.Equal(got, slices.Sorted(slices.Values(want))) {
				t.Errorf("%s: the workbench gets %v, want %v", name, wb[0].Events, want)
			}
			wantEntries := 2
			if kind != "" {
				wantEntries = 3
			}
			if len(cfg.Notifications.Backends) != wantEntries {
				t.Errorf("%s with channel %q: %d entries, want %d: the operator's channel is one more", name, kind, len(cfg.Notifications.Backends), wantEntries)
			}
			if err := route.CheckDestinations(cfg.Notifications.Backends, registry.Notifiers.Get); err != nil {
				t.Errorf("%s/%s: Sortie cannot build the workbench's entry: %v", name, kind, err)
			}
		}
	}
}

// Scenario: A question on taichi's Sortie, as it is now: a story that runs
// on taichi's worker parks on p1's Sortie, and Sortie posts the park to the
// workbench at once, with the question and the story, in the shape the
// workbench's tests use.
func TestStage0_AQuestionReachesTheWorkbench(t *testing.T) {
	const question = "Which cadence did the operator choose for the Authentik upgrade gate?"
	for _, name := range stage0Workflows {
		for _, rule := range []string{"build", "review"} {
			t.Run(name+"/"+rule, func(t *testing.T) {
				cfg := loadStage0(t, name)
				review := stage0Review(t, cfg)
				f := reviewExit(t, cfg, review, []string{reviewHuman})
				entry := f.state.Running[hopExitIssueID]
				if rule == "build" {
					entry.RuleName = rule
					entry.Issue.Labels = []string{reviewHuman}
					entry.DispatchLabels = slices.Clone(entry.Issue.Labels)
				}
				router := route.NewRouter(f.tracker, registry.Notifiers.Get)
				if err := router.Update(route.Inputs{Entries: cfg.Notifications.Backends}); err != nil {
					t.Fatal(err)
				}
				f.params.Router = router
				f.result.SoftStop = true
				f.result.SoftStopReason = string(workspace.StatusBlocked)
				f.result.SoftStopStatement = workspace.StatusStatement{Text: question}
				stage0Workbench.reset()
				f.exit()

				stopped := stage0Workbench.of(domain.EventSessionStopped)
				if len(stopped) != 1 {
					t.Fatalf("the workbench received %d session.stopped of %d posts, want 1", len(stopped), len(stage0Workbench.received()))
				}
				got := stopped[0]
				if got.body["identifier"] != entry.Identifier || !strings.Contains(fmt.Sprint(got.body["agent_text"]), question) {
					t.Errorf("the workbench received %s, want %s and the question", got.raw, entry.Identifier)
				}
				wantWorkbenchShape(t, domain.EventSessionStopped, got)
				if rule == "review" {
					// The review's failed criteria check is an escalation.
					esc := stage0Workbench.of(domain.EventSessionEscalated)
					if len(esc) != 1 {
						t.Fatalf("the workbench received %d session.escalated, want the failed criteria check", len(esc))
					}
					wantWorkbenchShape(t, domain.EventSessionEscalated, esc[0])
				}
			})
		}
	}
}

// writeWorkbenchFixtures writes what Sortie posted as the workbench's
// fixtures when STAGE0_WRITE_WORKBENCH_FIXTURES names their directory: how
// they are made again after Sortie's payload changes.
func writeWorkbenchFixtures(t *testing.T, posts []workbenchPost) {
	t.Helper()
	dir := os.Getenv("STAGE0_WRITE_WORKBENCH_FIXTURES")
	if dir == "" {
		return
	}
	for _, p := range posts {
		event, _ := p.body["event_type"].(string)
		if err := os.WriteFile(filepath.Join(dir, strings.ReplaceAll(event, ".", "_")+".json"), append(p.raw, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
