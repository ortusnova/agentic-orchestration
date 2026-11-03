// The kit's operator channel (agent-platform/run-supervision#A person is
// alerted, wherever the question is held), checked against Sortie itself:
// each of the kit's workflows, rendered with the channel entry that
// notify-channel.sh writes for each kind, loads with Sortie's own loader, and
// a story that stops for a person reaches the channel through Sortie's own
// router and notifier, with its question. dev/stage0/test-review-gate.sh
// compiles this file into Sortie's orchestrator package, beside
// stage0_review_test.go.

package orchestrator

import (
	"cmp"
	"encoding/json"
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

// channelBackend is the workflow's entry of that kind.
func channelBackend(t *testing.T, cfg config.ServiceConfig, kind string) config.NotificationBackend {
	t.Helper()
	for _, b := range cfg.Notifications.Backends {
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
		if len(cfg.Notifications.Backends) != 1 || cfg.Notifications.Backends[0].Kind != domain.TrackerCommentKind {
			t.Errorf("%s with no channel: notifications = %+v, want the tracker comments only", name, cfg.Notifications.Backends)
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
