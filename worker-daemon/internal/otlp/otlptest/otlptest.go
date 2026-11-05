// Package otlptest builds OTLP/HTTP export requests for tests, as an
// OpenTelemetry SDK sends them: one resource, one scope, and one span,
// log record or gauge point.
package otlptest

import (
	"encoding/binary"
	"encoding/json"
	"math"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp"
)

func tag(out []byte, num, typ uint64) []byte { return binary.AppendUvarint(out, num<<3|typ) }

func field(out []byte, num uint64, payload []byte) []byte {
	out = tag(out, num, 2)
	out = binary.AppendUvarint(out, uint64(len(payload)))
	return append(out, payload...)
}

func fixed64(out []byte, num, v uint64) []byte {
	return binary.LittleEndian.AppendUint64(tag(out, num, 1), v)
}

func kv(a otlp.Attr) []byte {
	return field(field(nil, 1, []byte(a.Key)), 2, field(nil, 1, []byte(a.Value)))
}

func kvs(out []byte, num uint64, attrs []otlp.Attr) []byte {
	for _, a := range attrs {
		out = field(out, num, kv(a))
	}
	return out
}

const unixNano = 1_700_000_000_000_000_000

// Protobuf is an export request of signal in the protobuf encoding: a
// resource with resource attributes, and one item named name (a span, a
// log record whose body is name, or a gauge) with attrs.
func Protobuf(signal string, resource []otlp.Attr, name string, attrs []otlp.Attr) []byte {
	var item []byte
	switch signal {
	case otlp.Traces:
		item = field(nil, 1, []byte("0123456789abcdef"))
		item = field(item, 2, []byte("01234567"))
		item = field(item, 5, []byte(name))
		item = fixed64(item, 7, unixNano)
		item = fixed64(item, 8, unixNano+1000)
		item = kvs(item, 9, attrs)
	case otlp.Logs:
		item = fixed64(nil, 1, unixNano)
		item = field(item, 5, field(nil, 1, []byte(name)))
		item = kvs(item, 6, attrs)
	default:
		point := kvs(nil, 7, attrs)
		point = fixed64(point, 3, unixNano)
		point = fixed64(point, 4, math.Float64bits(1.5))
		item = field(nil, 1, []byte(name))
		item = field(item, 5, field(nil, 1, point))
	}
	scoped := field(nil, 1, field(nil, 1, []byte("agent")))
	scoped = field(scoped, 2, item)
	holder := field(nil, 1, kvs(nil, 1, resource))
	holder = field(holder, 2, scoped)
	return field(nil, 1, holder)
}

// JSON is [Protobuf]'s request in the OTLP JSON encoding.
func JSON(signal string, resource []otlp.Attr, name string, attrs []otlp.Attr) []byte {
	jattrs := func(as []otlp.Attr) []any {
		out := []any{}
		for _, a := range as {
			out = append(out, map[string]any{"key": a.Key, "value": map[string]any{"stringValue": a.Value}})
		}
		return out
	}
	var list, scopes, items string
	var item map[string]any
	switch signal {
	case otlp.Traces:
		list, scopes, items = "resourceSpans", "scopeSpans", "spans"
		item = map[string]any{"traceId": "30313233343536373839616263646566", "spanId": "3031323334353637", "name": name,
			"startTimeUnixNano": "1700000000000000000", "endTimeUnixNano": "1700000000000001000", "attributes": jattrs(attrs)}
	case otlp.Logs:
		list, scopes, items = "resourceLogs", "scopeLogs", "logRecords"
		item = map[string]any{"timeUnixNano": "1700000000000000000", "body": map[string]any{"stringValue": name}, "attributes": jattrs(attrs)}
	default:
		list, scopes, items = "resourceMetrics", "scopeMetrics", "metrics"
		item = map[string]any{"name": name, "gauge": map[string]any{"dataPoints": []any{
			map[string]any{"timeUnixNano": "1700000000000000000", "asDouble": 1.5, "attributes": jattrs(attrs)}}}}
	}
	b, _ := json.Marshal(map[string]any{list: []any{map[string]any{
		"resource": map[string]any{"attributes": jattrs(resource)},
		scopes:     []any{map[string]any{"scope": map[string]any{"name": "agent"}, items: []any{item}}},
	}}})
	return b
}
