// Package otlp exports the broker's telemetry spans as OTLP/HTTP trace
// requests with JSON encoding.
package otlp

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/secrets"
	"github.com/ortusnova/agentic-orchestration/acp-broker/internal/telemetry"
)

// ServiceName names the broker in every export's resource.
const ServiceName = "acp-broker"

// maxTitleRunes bounds an exported tool title.
const maxTitleRunes = 200

// OTLP enumeration values, which the JSON encoding carries as numbers.
const (
	spanKindInternal = 1
	statusCodeError  = 2
)

// Encoder turns spans into an OTLP ExportTraceServiceRequest in its JSON
// encoding. Every string it exports is masked of the secrets registered
// with the secrets package first, so no attribute can skip masking.
type Encoder struct {
	// ServiceVersion is reported as the resource's service.version.
	ServiceVersion string

	// ContentMax bounds each exported transcript value in bytes.
	ContentMax int
}

// Encode returns the export request for spans.
func (e Encoder) Encode(spans []telemetry.Span) ([]byte, error) {
	out := make([]span, 0, len(spans))
	for _, sp := range spans {
		out = append(out, e.span(sp))
	}
	req := exportRequest{ResourceSpans: []resourceSpans{{
		Resource: resource{Attributes: e.attrs(
			str("service.name", ServiceName),
			str("service.version", e.ServiceVersion),
		)},
		ScopeSpans: []scopeSpans{{Scope: scope{Name: ServiceName, Version: e.clean(e.ServiceVersion, false)}, Spans: out}},
	}}}
	return json.Marshal(req)
}

func (e Encoder) span(sp telemetry.Span) span {
	s := span{
		TraceID: hex.EncodeToString(sp.TraceID[:]),
		SpanID:  hex.EncodeToString(sp.SpanID[:]),
		Kind:    spanKindInternal,
		Start:   unixNano(sp.Start),
		End:     unixNano(sp.End),
	}
	if sp.ParentID != ([8]byte{}) {
		s.ParentSpanID = hex.EncodeToString(sp.ParentID[:])
	}
	kv := e.sessionAttrs(sp)
	if sp.Kind == telemetry.KindTurn {
		s.Name = "turn " + string(sp.Origin)
		kv = append(kv, e.turnAttrs(sp)...)
		for _, ev := range sp.SteerEvents {
			s.Events = append(s.Events, event{Time: unixNano(ev.Time), Name: "steer", Attributes: e.attrs(
				str("acp.steer.outcome", ev.Outcome),
				integer("acp.steer.count", int64(ev.Count)),
				str("acp.steer.error", ev.Error),
			)})
		}
	} else {
		s.Name = "tool"
		if sp.ToolKind != "" {
			s.Name = "tool " + e.clean(sp.ToolKind, false)
		}
		kv = append(kv, e.toolAttrs(sp)...)
	}
	if sp.Unfinished {
		kv = append(kv, boolean("acp.unfinished", true))
	}
	if sp.Input != "" || sp.InputCut {
		kv = append(kv, content("langfuse.observation.input", sp.Input, sp.InputCut))
	}
	if sp.Output != "" || sp.OutputCut {
		kv = append(kv, content("langfuse.observation.output", sp.Output, sp.OutputCut))
	}
	s.Attributes = e.attrs(kv...)
	if sp.Error != "" {
		s.Status = &status{Code: statusCodeError, Message: e.clean(sp.Error, false)}
	}
	return s
}

// sessionAttrs are the labels every span carries, so that Langfuse can
// filter and group observations as well as traces.
func (e Encoder) sessionAttrs(sp telemetry.Span) []attr {
	l := sp.Session.Labels
	sessionID := l.DispatchID
	if sessionID == "" {
		sessionID = sp.Session.ID
	}
	return []attr{
		str("langfuse.session.id", sessionID),
		str("langfuse.trace.name", "agent turn ("+string(sp.Origin)+")"),
		str("langfuse.trace.metadata.issue", l.Issue),
		str("langfuse.trace.metadata.rule", l.Rule),
		str("langfuse.trace.metadata.chain_id", l.ChainID),
		str("langfuse.trace.metadata.dispatch_id", l.DispatchID),
		str("langfuse.trace.metadata.attempt", l.Attempt),
		str("langfuse.trace.metadata.origin", string(sp.Origin)),
		str("langfuse.trace.metadata.agent", sp.Session.AgentName),
		str("sortie.issue.identifier", l.Issue),
		str("sortie.rule", l.Rule),
		str("sortie.chain.id", l.ChainID),
		str("sortie.dispatch.id", l.DispatchID),
		str("sortie.attempt", l.Attempt),
		str("acp.session.id", sp.Session.ID),
		str("acp.agent.name", sp.Session.AgentName),
		str("acp.agent.version", sp.Session.AgentVersion),
	}
}

func (e Encoder) turnAttrs(sp telemetry.Span) []attr {
	kv := []attr{
		str("langfuse.observation.type", "generation"),
		str("acp.turn.origin", string(sp.Origin)),
		str("acp.turn.stop_reason", sp.StopReason),
		str("acp.turn.cancelled_for", sp.CancelledFor),
		str("gen_ai.request.model", sp.Model),
		str("langfuse.observation.model.name", sp.Model),
	}
	if u := sp.Usage; u != nil {
		details := map[string]int64{}
		add := func(key string, v *int64) {
			if v != nil {
				details[key] = *v
			}
		}
		add("input", u.Input)
		add("output", u.Output)
		add("cache_read_input_tokens", u.CachedRead)
		add("cache_creation_input_tokens", u.CachedWrite)
		add("total", u.Total)
		add("reasoning", u.Reasoning)
		b, _ := json.Marshal(details)
		kv = append(kv, str("langfuse.observation.usage_details", string(b)))
		if u.Input != nil {
			kv = append(kv, integer("gen_ai.usage.input_tokens", *u.Input))
		}
		if u.Output != nil {
			kv = append(kv, integer("gen_ai.usage.output_tokens", *u.Output))
		}
	} else {
		kv = append(kv, boolean("acp.usage.reported", false))
	}
	if sp.SteerCount > 0 {
		kv = append(kv, integer("acp.steer.count", int64(sp.SteerCount)))
	}
	if sp.ToolsDropped > 0 {
		kv = append(kv, integer("acp.tool.dropped", int64(sp.ToolsDropped)))
	}
	return kv
}

func (e Encoder) toolAttrs(sp telemetry.Span) []attr {
	return []attr{
		str("langfuse.observation.type", "tool"),
		str("acp.tool.call_id", sp.CallID),
		str("acp.tool.kind", sp.ToolKind),
		title("acp.tool.title", sp.Title),
		str("acp.tool.status", sp.Status),
	}
}

// attrs cleans and encodes kv, leaving out empty strings, so that an unset
// label is omitted rather than sent empty.
func (e Encoder) attrs(kv ...attr) []keyValue {
	out := make([]keyValue, 0, len(kv))
	for _, a := range kv {
		var v anyValue
		switch a.kind {
		case kindString, kindTitle, kindContent:
			s := e.value(a)
			if s == "" {
				continue
			}
			v.String = &s
		case kindInt:
			n := strconv.FormatInt(a.n, 10)
			v.Int = &n
		case kindBool:
			b := a.b
			v.Bool = &b
		}
		out = append(out, keyValue{Key: a.key, Value: v})
	}
	return out
}

func (e Encoder) value(a attr) string {
	switch a.kind {
	case kindTitle:
		return truncateRunes(e.clean(a.s, false), maxTitleRunes)
	case kindContent:
		s := e.clean(a.s, a.cut)
		limit := e.ContentMax
		if limit <= 0 {
			limit = telemetry.DefaultContentMax
		}
		if a.cut || len(s) > limit {
			return truncateBytes(s, limit) + truncatedMarker
		}
		return s
	}
	return e.clean(a.s, false)
}

// clean masks s. A value the recorder kept only the start of may end in
// the first part of a secret, which MaskTruncated removes too.
func (e Encoder) clean(s string, cut bool) string {
	if cut {
		return secrets.MaskTruncated(s)
	}
	return secrets.Mask(s)
}

// truncatedMarker ends a transcript value cut to the export limit.
const truncatedMarker = "…[truncated]"

func unixNano(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.UnixNano(), 10)
}

func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}

// truncateBytes cuts s to at most n bytes without splitting a rune.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && !isRuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// attr is one attribute before encoding.
type attr struct {
	key  string
	kind attrKind
	s    string
	n    int64
	b    bool
	cut  bool
}

type attrKind int

const (
	kindString attrKind = iota
	kindTitle
	kindContent
	kindInt
	kindBool
)

func str(key, s string) attr           { return attr{key: key, kind: kindString, s: s} }
func title(key, s string) attr         { return attr{key: key, kind: kindTitle, s: s} }
func integer(key string, n int64) attr { return attr{key: key, kind: kindInt, n: n} }
func boolean(key string, b bool) attr  { return attr{key: key, kind: kindBool, b: b} }
func content(key, s string, cut bool) attr {
	return attr{key: key, kind: kindContent, s: s, cut: cut}
}

// The OTLP/JSON shapes. Trace and span IDs are lowercase hex, 64-bit
// integers are decimal strings, and enumerations are numbers.
type exportRequest struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type resourceSpans struct {
	Resource   resource     `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type resource struct {
	Attributes []keyValue `json:"attributes"`
}

type scopeSpans struct {
	Scope scope  `json:"scope"`
	Spans []span `json:"spans"`
}

type scope struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type span struct {
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	ParentSpanID string     `json:"parentSpanId,omitempty"`
	Name         string     `json:"name"`
	Kind         int        `json:"kind"`
	Start        string     `json:"startTimeUnixNano"`
	End          string     `json:"endTimeUnixNano"`
	Attributes   []keyValue `json:"attributes"`
	Events       []event    `json:"events,omitempty"`
	Status       *status    `json:"status,omitempty"`
}

type event struct {
	Time       string     `json:"timeUnixNano"`
	Name       string     `json:"name"`
	Attributes []keyValue `json:"attributes"`
}

type status struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

type keyValue struct {
	Key   string   `json:"key"`
	Value anyValue `json:"value"`
}

type anyValue struct {
	String *string `json:"stringValue,omitempty"`
	Int    *string `json:"intValue,omitempty"`
	Bool   *bool   `json:"boolValue,omitempty"`
}
