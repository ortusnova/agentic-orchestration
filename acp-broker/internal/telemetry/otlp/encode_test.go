package otlp

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
)

var update = flag.Bool("update", false, "rewrite the golden files")

var (
	t0      = time.Date(2026, 10, 11, 21, 0, 0, 0, time.UTC)
	traceA  = [16]byte{0x4b, 0xf9, 0x2f, 0x35, 0x77, 0xb3, 0x4d, 0xa6, 0xa3, 0xce, 0x92, 0x9d, 0x0e, 0x0e, 0x47, 0x36}
	turnA   = [8]byte{0x00, 0xf0, 0x67, 0xaa, 0x0b, 0xa9, 0x02, 0xb7}
	labels  = telemetry.Labels{Issue: "LIVE-1", Rule: "implement", ChainID: "c-42", DispatchID: "D1", Attempt: "2"}
	session = telemetry.Session{ID: "s1", AgentName: "opencode", AgentVersion: "1.18.29", Labels: labels}
)

func n(v int64) *int64 { return new(v) }

// goldenCases are the export requests the encoder must produce.
func goldenCases() map[string][]telemetry.Span {
	return map[string][]telemetry.Span{
		"driver-turn-with-usage-and-tools": {
			{
				Kind: telemetry.KindTool, TraceID: traceA, ParentID: turnA, SpanID: [8]byte{1, 1, 1, 1, 1, 1, 1, 1},
				Start: t0.Add(time.Second), End: t0.Add(2 * time.Second), Session: session, Origin: telemetry.OriginDriver,
				CallID: "call_1", ToolKind: "read", Title: "Read main.go", Status: "completed",
			},
			{
				Kind: telemetry.KindTool, TraceID: traceA, ParentID: turnA, SpanID: [8]byte{2, 2, 2, 2, 2, 2, 2, 2},
				Start: t0.Add(3 * time.Second), End: t0.Add(4 * time.Second), Session: session, Origin: telemetry.OriginDriver,
				CallID: "call_2", ToolKind: "execute", Title: "go test ./...", Status: "failed", Error: "tool call failed",
			},
			{
				Kind: telemetry.KindTurn, TraceID: traceA, SpanID: turnA, Start: t0, End: t0.Add(5 * time.Second),
				Session: session, Origin: telemetry.OriginDriver, StopReason: "end_turn", Model: "anthropic/claude-haiku-5-5",
				Usage:       &telemetry.Usage{Input: n(4), Output: n(102), CachedRead: n(53643), CachedWrite: n(307), Total: n(54056)},
				SteerEvents: []telemetry.SteerEvent{{Time: t0.Add(2500 * time.Millisecond), Outcome: "injected", Count: 2}},
			},
		},
		"steer-turn-without-usage": {
			{
				Kind: telemetry.KindTurn, TraceID: [16]byte{0xaa, 15: 0x01}, SpanID: [8]byte{0xbb, 7: 0x02}, Start: t0, End: t0.Add(time.Second),
				Session: telemetry.Session{ID: "s1", AgentName: "pi-acp"}, Origin: telemetry.OriginSteer, StopReason: "end_turn",
				SteerCount: 2,
			},
		},
		"failed-human-turn": {
			{
				Kind: telemetry.KindTurn, TraceID: [16]byte{0xcc, 15: 0x03}, SpanID: [8]byte{0xdd, 7: 0x04}, Start: t0, End: t0.Add(time.Second),
				Session: session, Origin: telemetry.OriginHuman, Model: "haiku", Error: "model overloaded", Unfinished: true,
				CancelledFor: telemetry.CancelledForInterrupt,
			},
		},
	}
}

func TestEncode_Golden(t *testing.T) {
	t.Parallel()

	enc := Encoder{ServiceVersion: "test"}
	for name, spans := range goldenCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			raw, err := enc.Encode(spans)
			if err != nil {
				t.Fatal(err)
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, raw, "", "  "); err != nil {
				t.Fatal(err)
			}
			pretty.WriteByte('\n')
			path := filepath.Join("testdata", name+".json")
			if *update {
				if err := os.WriteFile(path, pretty.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(pretty.Bytes(), want) {
				t.Errorf("Encode(%s) differs from %s:\n%s", name, path, pretty.String())
			}
		})
	}
}

// TestEncode_OTLPJSONRules checks the encoding rules where OTLP/JSON
// departs from the usual protobuf JSON mapping, on every golden case.
func TestEncode_OTLPJSONRules(t *testing.T) {
	t.Parallel()

	hexID := map[string]*regexp.Regexp{
		"traceId":      regexp.MustCompile(`^[0-9a-f]{32}$`),
		"spanId":       regexp.MustCompile(`^[0-9a-f]{16}$`),
		"parentSpanId": regexp.MustCompile(`^[0-9a-f]{16}$`),
	}
	decimal := regexp.MustCompile(`^[0-9]+$`)
	for name, spans := range goldenCases() {
		raw, err := Encoder{}.Encode(spans)
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []map[string]any `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatal(err)
		}
		for _, sp := range req.ResourceSpans[0].ScopeSpans[0].Spans {
			for key, re := range hexID {
				if v, ok := sp[key]; ok && !re.MatchString(v.(string)) {
					t.Errorf("%s: %s = %v, want lowercase hex", name, key, v)
				}
			}
			for _, key := range []string{"startTimeUnixNano", "endTimeUnixNano"} {
				if v, ok := sp[key].(string); !ok || !decimal.MatchString(v) {
					t.Errorf("%s: %s = %v, want a decimal string", name, key, sp[key])
				}
			}
			if _, ok := sp["kind"].(float64); !ok {
				t.Errorf("%s: kind = %v, want a number", name, sp["kind"])
			}
			for _, a := range sp["attributes"].([]any) {
				v := a.(map[string]any)["value"].(map[string]any)
				if iv, ok := v["intValue"]; ok {
					if s, isString := iv.(string); !isString || !decimal.MatchString(s) {
						t.Errorf("%s: intValue %v, want a decimal string", name, iv)
					}
				}
				if sv, ok := v["stringValue"]; ok && sv == "" {
					t.Errorf("%s: empty string attribute %v, want it omitted", name, a)
				}
			}
		}
	}
}

func TestEncode_TitleAndContentLimits(t *testing.T) {
	t.Parallel()

	long := string(bytes.Repeat([]byte("é"), 300))
	spans := []telemetry.Span{
		{Kind: telemetry.KindTool, Title: long, Input: long, CallID: "c"},
		{Kind: telemetry.KindTurn, Output: "short but cut", OutputCut: true},
	}
	raw, err := Encoder{ContentMax: 101}.Encode(spans)
	if err != nil {
		t.Fatal(err)
	}
	var req exportRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	get := func(sp span, key string) string {
		for _, kv := range sp.Attributes {
			if kv.Key == key && kv.Value.String != nil {
				return *kv.Value.String
			}
		}
		return ""
	}
	got := req.ResourceSpans[0].ScopeSpans[0].Spans
	if title := get(got[0], "acp.tool.title"); len([]rune(title)) != maxTitleRunes+1 {
		t.Errorf("title has %d runes, want %d and an ellipsis", len([]rune(title)), maxTitleRunes)
	}
	// 101 bytes of two-byte runes cut to 100, without splitting a rune.
	if in := get(got[0], "langfuse.observation.input"); in != long[:100]+truncatedMarker {
		t.Errorf("input = %q, want the first 50 runes and the cut marker", in)
	}
	if out := get(got[1], "langfuse.observation.output"); out != "short but cut"+truncatedMarker {
		t.Errorf("output = %q, want the kept text marked as cut", out)
	}
}
