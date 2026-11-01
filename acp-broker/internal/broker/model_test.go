package broker_test

import (
	"sync"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/broker"
)

// sessionInfos records what the broker reports through OnSession.
type sessionInfos struct {
	mu    sync.Mutex
	infos []broker.SessionInfo
}

func (s *sessionInfos) record(info broker.SessionInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.infos = append(s.infos, info)
}

func (s *sessionInfos) last() broker.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.infos) == 0 {
		return broker.SessionInfo{}
	}
	return s.infos[len(s.infos)-1]
}

func (s *sessionInfos) waitModel(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.last().Model == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("model = %q, want %q", s.last().Model, want)
}

func configOptions(model string) []any {
	opts := []any{map[string]any{"id": "mode", "category": "mode", "type": "select", "currentValue": "default"}}
	if model != "" {
		opts = append(opts, map[string]any{"id": "model", "category": "model", "type": "select", "currentValue": model})
	}
	return opts
}

func TestModel_TrackedFromEverySource(t *testing.T) {
	t.Parallel()

	infos := &sessionInfos{}
	s := startBroker(t, broker.Config{OnSession: infos.record})
	// startBroker's session/new reported no configuration: no model.
	deadline := time.Now().Add(2 * time.Second)
	for infos.last().SessionID != "s1" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := infos.last(); got.SessionID != "s1" || got.Model != "" {
		t.Fatalf("info after a session without configuration = %+v, want s1 and no model", got)
	}

	reply := func(id int, method string, result map[string]any) {
		s.driver.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": map[string]any{"sessionId": "s1"}})
		s.agent.nextWhere(isMethod(method))
		s.agent.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		s.driver.next()
	}
	update := func(opts []any) {
		s.agent.send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
			"sessionId": "s1", "update": map[string]any{"sessionUpdate": "config_option_update", "configOptions": opts},
		}})
	}

	reply(2, "session/new", map[string]any{"sessionId": "s1", "configOptions": configOptions("anthropic/claude-haiku-5-5")})
	infos.waitModel(t, "anthropic/claude-haiku-5-5")

	reply(3, "session/load", map[string]any{"configOptions": configOptions("haiku")})
	infos.waitModel(t, "haiku")

	reply(4, "session/set_config_option", map[string]any{"configOptions": configOptions("sonnet")})
	infos.waitModel(t, "sonnet")

	update(configOptions("opus"))
	infos.waitModel(t, "opus")

	// A result without configuration options leaves the model alone.
	reply(5, "session/load", map[string]any{})
	time.Sleep(20 * time.Millisecond)
	infos.waitModel(t, "opus")

	// A configuration that no longer names a model means the agent reports none.
	update(configOptions(""))
	infos.waitModel(t, "")
}
