// Package otlp rewrites the OTLP/HTTP export requests a job's machine
// sends, in their protobuf or JSON encoding: it stamps each resource with
// the attempt's attributes and masks every string of the request.
//
// It knows the messages of opentelemetry-proto's trace, logs and metrics
// export requests well enough to find each resource and each string; a
// length-delimited field it does not know as a message is masked as text,
// so that no field can carry a known value out.
package otlp

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"slices"
)

// Signals and their export paths, /v1/{signal}.
const (
	Traces  = "traces"
	Logs    = "logs"
	Metrics = "metrics"
)

// Signals lists every signal a receiver takes.
var Signals = []string{Traces, Logs, Metrics}

// Content types of OTLP/HTTP.
const (
	ContentTypeProtobuf = "application/x-protobuf"
	ContentTypeJSON     = "application/json"
)

// Attr is a resource attribute with a string value.
type Attr struct{ Key, Value string }

// Encoding returns the encoding a Content-Type names: ContentTypeProtobuf
// or ContentTypeJSON, or an error for any other.
func Encoding(contentType string) (string, error) {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return "", fmt.Errorf("otlp: content type %q: %w", contentType, err)
	}
	switch mt {
	case ContentTypeProtobuf, "application/protobuf":
		return ContentTypeProtobuf, nil
	case ContentTypeJSON:
		return ContentTypeJSON, nil
	}
	return "", fmt.Errorf("otlp: content type %q is neither %s nor %s", mt, ContentTypeProtobuf, ContentTypeJSON)
}

// Rewrite returns the export request body of signal, in encoding (see
// [Encoding]), with attrs set on each resource, over any value the
// request gave them, and mask applied to every other string.
func Rewrite(signal, encoding string, body []byte, attrs []Attr, mask func(string) string) ([]byte, error) {
	if !slices.Contains(Signals, signal) {
		return nil, fmt.Errorf("otlp: unknown signal %q", signal)
	}
	switch encoding {
	case ContentTypeProtobuf:
		return rewriteProto(requestSchema(signal), body, attrs, mask)
	case ContentTypeJSON:
		return rewriteJSON(signal, body, attrs, mask)
	}
	return nil, fmt.Errorf("otlp: unknown encoding %q", encoding)
}

// Resource is what [Inspect] reads of one resource's part of a request.
type Resource struct {
	// Attributes are the resource's attributes with a string value.
	Attributes map[string]string
	// Strings are every string under the resource's part, its
	// resource included, in order.
	Strings []string
}

// Inspect reads each resource of an export request: its string
// attributes and every string it carries.
func Inspect(signal, encoding string, body []byte) ([]Resource, error) {
	if !slices.Contains(Signals, signal) {
		return nil, fmt.Errorf("otlp: unknown signal %q", signal)
	}
	switch encoding {
	case ContentTypeProtobuf:
		return inspectProto(requestSchema(signal), body)
	case ContentTypeJSON:
		return inspectJSON(signal, body)
	}
	return nil, fmt.Errorf("otlp: unknown encoding %q", encoding)
}

// --- JSON ---

// resourceListKeys are the members of an export request that hold its
// resources, in the OTLP JSON encoding (lowerCamelCase) and as the
// protobuf JSON mapping also accepts them.
func resourceListKeys(signal string) []string {
	switch signal {
	case Traces:
		return []string{"resourceSpans", "resource_spans"}
	case Logs:
		return []string{"resourceLogs", "resource_logs"}
	}
	return []string{"resourceMetrics", "resource_metrics"}
}

func decodeJSON(body []byte) (map[string]any, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, fmt.Errorf("otlp: JSON: %w", err)
	}
	if d.More() {
		return nil, errors.New("otlp: JSON: more than one value")
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("otlp: JSON: the request is not an object")
	}
	return obj, nil
}

func rewriteJSON(signal string, body []byte, attrs []Attr, mask func(string) string) ([]byte, error) {
	req, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	out, _ := maskJSON(req, mask).(map[string]any)
	for _, k := range resourceListKeys(signal) {
		list, ok := out[k].([]any)
		if !ok {
			continue
		}
		for i, rs := range list {
			obj, ok := rs.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("otlp: JSON: %s[%d] is not an object", k, i)
			}
			res, _ := obj["resource"].(map[string]any)
			if res == nil {
				res = map[string]any{}
			}
			kept := []any{}
			if old, ok := res["attributes"].([]any); ok {
				for _, kv := range old {
					if m, ok := kv.(map[string]any); ok && stamped(attrs, fmt.Sprint(m["key"])) {
						continue
					}
					kept = append(kept, kv)
				}
			}
			for _, a := range attrs {
				kept = append(kept, map[string]any{"key": a.Key, "value": map[string]any{"stringValue": a.Value}})
			}
			res["attributes"] = kept
			obj["resource"] = res
		}
	}
	return json.Marshal(out)
}

// maskJSON masks every string of v, member names included.
func maskJSON(v any, mask func(string) string) any {
	switch x := v.(type) {
	case string:
		return mask(x)
	case []any:
		for i := range x {
			x[i] = maskJSON(x[i], mask)
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[mask(k)] = maskJSON(e, mask)
		}
		return out
	}
	return v
}

func stamped(attrs []Attr, key string) bool {
	return slices.ContainsFunc(attrs, func(a Attr) bool { return a.Key == key })
}

func inspectJSON(signal string, body []byte) ([]Resource, error) {
	req, err := decodeJSON(body)
	if err != nil {
		return nil, err
	}
	var out []Resource
	for _, k := range resourceListKeys(signal) {
		list, _ := req[k].([]any)
		for _, rs := range list {
			r := Resource{Attributes: map[string]string{}}
			jsonStrings(rs, &r.Strings)
			obj, _ := rs.(map[string]any)
			res, _ := obj["resource"].(map[string]any)
			attrs, _ := res["attributes"].([]any)
			for _, kv := range attrs {
				m, _ := kv.(map[string]any)
				key, _ := m["key"].(string)
				val, _ := m["value"].(map[string]any)
				if s, ok := val["stringValue"].(string); ok {
					r.Attributes[key] = s
				}
			}
			out = append(out, r)
		}
	}
	return out, nil
}

func jsonStrings(v any, out *[]string) {
	switch x := v.(type) {
	case string:
		*out = append(*out, x)
	case []any:
		for _, e := range x {
			jsonStrings(e, out)
		}
	case map[string]any:
		for _, k := range slices.Sorted(maps.Keys(x)) {
			*out = append(*out, k)
			jsonStrings(x[k], out)
		}
	}
}
