package otlp_test

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/otlp/otlptest"
	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/secret"
)

const token = "0f3c9a5e7b1d4f2a8c6e0b9d7f5a3c1e"

var stamp = []otlp.Attr{{Key: "sortie.issue.identifier", Value: "ONS-7"}, {Key: "sortie.dispatch.id", Value: "D1"}, {Key: "sortie.attempt", Value: "2"}}

func masker() func(string) string {
	m := &secret.Masker{}
	m.AddValue(token)
	return m.Mask
}

func TestRewriteStampsAndMasksEachSignal(t *testing.T) {
	for _, sig := range otlp.Signals {
		for enc, build := range map[string]func(string, []otlp.Attr, string, []otlp.Attr) []byte{
			otlp.ContentTypeProtobuf: otlptest.Protobuf, otlp.ContentTypeJSON: otlptest.JSON,
		} {
			t.Run(sig+" "+enc, func(t *testing.T) {
				resource := []otlp.Attr{{Key: "service.name", Value: "opencode"}, {Key: "sortie.attempt", Value: "9"}, {Key: "host", Value: "h-" + token}}
				body := build(sig, resource, "step "+token, []otlp.Attr{{Key: "tool.input", Value: "Bearer " + token}})
				out, err := otlp.Rewrite(sig, enc, body, stamp, masker())
				if err != nil {
					t.Fatal(err)
				}
				if bytes.Contains(out, []byte(token)) {
					t.Fatalf("the token is still in the request: %q", out)
				}
				rs, err := otlp.Inspect(sig, enc, out)
				if err != nil || len(rs) != 1 {
					t.Fatalf("Inspect: %v, %d resources", err, len(rs))
				}
				want := map[string]string{"service.name": "opencode", "host": "h-[redacted]",
					"sortie.issue.identifier": "ONS-7", "sortie.dispatch.id": "D1", "sortie.attempt": "2"}
				for k, v := range want {
					if got := rs[0].Attributes[k]; got != v {
						t.Errorf("resource attribute %s = %q, want %q", k, got, v)
					}
				}
				all := strings.Join(rs[0].Strings, "\n")
				for _, s := range []string{"step [redacted]", "Bearer [redacted]", "agent"} {
					if !strings.Contains(all, s) {
						t.Errorf("the request lost %q: %q", s, all)
					}
				}
			})
		}
	}
}

func TestRewriteAddsAResource(t *testing.T) {
	// A ResourceSpans with only its scope spans: no resource at all.
	bare := lenField(1, lenField(2, lenField(2, lenField(5, []byte("s")))))
	out, err := otlp.Rewrite(otlp.Traces, otlp.ContentTypeProtobuf, bare, stamp, masker())
	if err != nil {
		t.Fatal(err)
	}
	rs, _ := otlp.Inspect(otlp.Traces, otlp.ContentTypeProtobuf, out)
	if len(rs) != 1 || rs[0].Attributes["sortie.attempt"] != "2" {
		t.Fatalf("resources %+v", rs)
	}
	jsonOut, err := otlp.Rewrite(otlp.Logs, otlp.ContentTypeJSON, []byte(`{"resourceLogs":[{"scopeLogs":[]}]}`), stamp, masker())
	if err != nil {
		t.Fatal(err)
	}
	rs, _ = otlp.Inspect(otlp.Logs, otlp.ContentTypeJSON, jsonOut)
	if len(rs) != 1 || rs[0].Attributes["sortie.dispatch.id"] != "D1" {
		t.Fatalf("resources %+v from %s", rs, jsonOut)
	}
}

func TestRewriteMasksNestedAndUnknownFields(t *testing.T) {
	// A span attribute whose value is a kvlist holding an array holding
	// the token, and an unknown field 99 of the span holding it too.
	str := func(s string) []byte { return lenField(1, []byte(s)) }
	array := lenField(5, lenField(1, str(token)))
	kvlist := lenField(6, lenField(1, append(lenField(1, []byte("inner")), lenField(2, array)...)))
	span := append(lenField(5, []byte("s")), lenField(9, append(lenField(1, []byte("outer")), lenField(2, kvlist)...))...)
	span = append(span, lenField(99, []byte("x"+token))...)
	req := lenField(1, lenField(2, lenField(2, span)))
	out, err := otlp.Rewrite(otlp.Traces, otlp.ContentTypeProtobuf, req, nil, masker())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(token)) || bytes.Count(out, []byte("[redacted]")) != 2 {
		t.Fatalf("not masked: %q", out)
	}
}

func TestRewriteRefusesMalformed(t *testing.T) {
	for _, c := range []struct{ enc, body string }{
		{otlp.ContentTypeProtobuf, "\x0a\x05ab"},
		{otlp.ContentTypeProtobuf, "\x0b"},
		{otlp.ContentTypeJSON, "{"},
		{otlp.ContentTypeJSON, "[]"},
	} {
		if _, err := otlp.Rewrite(otlp.Traces, c.enc, []byte(c.body), stamp, masker()); err == nil {
			t.Errorf("%s %q: no error", c.enc, c.body)
		}
	}
	if _, err := otlp.Encoding("text/plain"); err == nil {
		t.Error("text/plain is an encoding")
	}
	if enc, err := otlp.Encoding("application/json; charset=utf-8"); err != nil || enc != otlp.ContentTypeJSON {
		t.Errorf("Encoding: %q, %v", enc, err)
	}
}

func lenField(num uint64, payload []byte) []byte {
	out := binary.AppendUvarint(nil, num<<3|2)
	out = binary.AppendUvarint(out, uint64(len(payload)))
	return append(out, payload...)
}
