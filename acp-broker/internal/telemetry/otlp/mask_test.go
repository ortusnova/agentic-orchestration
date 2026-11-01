package otlp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/secrets"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
)

// The secrets below are unique to this file, so registering them in the
// process-wide registry cannot change what other tests export.

func encodeString(t *testing.T, enc Encoder, spans ...telemetry.Span) string {
	t.Helper()
	raw, err := enc.Encode(spans)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestMask_SessionTokenInAToolTitle(t *testing.T) {
	t.Parallel()

	token := "_maskTestSessionToken-4f1c9a"
	RegisterSecrets(token, nil, nil)
	out := encodeString(t, Encoder{}, telemetry.Span{
		Kind: telemetry.KindTool, CallID: "c1", Title: "curl -H 'Authorization: Bearer " + token + "' http://127.0.0.1:7678",
	})
	if strings.Contains(out, token) || !strings.Contains(out, "Bearer "+secrets.Marker) {
		t.Errorf("export = %s, want the token replaced by the marker", out)
	}
}

func TestMask_SecretNamedVariableInAnErrorMessage(t *testing.T) {
	t.Parallel()

	value := "ghp_maskTestGitHubToken0123456789"
	RegisterSecrets("", []string{"GITHUB_TOKEN=" + value, "PATH_HINT=not-a-secret-value"}, nil)
	out := encodeString(t, Encoder{}, telemetry.Span{
		Kind: telemetry.KindTurn, Origin: telemetry.OriginDriver, Error: "push failed: " + value + " was rejected",
		Title: "not-a-secret-value",
	})
	if strings.Contains(out, value) || !strings.Contains(out, "push failed: "+secrets.Marker) {
		t.Errorf("export = %s, want the GITHUB_TOKEN value masked in the status message", out)
	}
}

func TestMask_HeaderValues(t *testing.T) {
	t.Parallel()

	RegisterSecrets("", nil, http.Header{"Authorization": {"Basic bWFza1Rlc3Q6aGVhZGVyLXNlY3JldA=="}})
	out := encodeString(t, Encoder{}, telemetry.Span{
		Kind: telemetry.KindTurn, Output: "the header was Basic bWFza1Rlc3Q6aGVhZGVyLXNlY3JldA==",
	})
	if strings.Contains(out, "bWFza1Rlc3Q6aGVhZGVyLXNlY3JldA==") {
		t.Errorf("export = %s, want the header's credential masked", out)
	}
}

func TestMask_CutValueDropsATrailingSecretFragment(t *testing.T) {
	t.Parallel()

	secret := "maskTestFragmentSecret-77ab31"
	secrets.Add("test", secret)
	// The recorder kept only the start of a longer text, ending inside the secret.
	out := encodeString(t, Encoder{}, telemetry.Span{
		Kind: telemetry.KindTurn, Output: "the key is " + secret[:15], OutputCut: true,
	})
	if strings.Contains(out, secret[:15]) || !strings.Contains(out, "the key is "+truncatedMarker) {
		t.Errorf("export = %s, want the fragment removed and the value marked as cut", out)
	}
}

func TestMask_EveryExportedString(t *testing.T) {
	t.Parallel()

	secret := "maskTestEverywhere-c0ffee99"
	secrets.Add("test", secret)
	s := "x " + secret + " y"
	session := telemetry.Session{ID: s, AgentName: s, AgentVersion: s, Labels: telemetry.Labels{Issue: s, Rule: s, ChainID: s, DispatchID: s, Attempt: s}}
	out := encodeString(t, Encoder{ServiceVersion: s},
		telemetry.Span{
			Kind: telemetry.KindTurn, Session: session, Origin: telemetry.OriginHuman, StopReason: s, CancelledFor: s,
			Model: s, Error: s, Input: s, Output: s,
			SteerEvents: []telemetry.SteerEvent{{Outcome: s, Error: s, Count: 1}},
		},
		telemetry.Span{
			Kind: telemetry.KindTool, Session: session, CallID: s, ToolKind: s, Title: s, Status: s,
			Error: s, Input: s, Output: s, ParentID: [8]byte{1},
		},
	)
	if strings.Contains(out, secret) {
		t.Errorf("a secret left in the export: %s", out)
	}
}
