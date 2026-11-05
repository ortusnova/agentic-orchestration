package otlp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// schema is one protobuf message of opentelemetry-proto: which of its
// fields hold a message. Every other length-delimited field is a string,
// bytes, or a packed number, and is masked as text.
type schema struct {
	msgs map[uint64]*schema
	// holder is a ResourceSpans, ResourceLogs or ResourceMetrics, whose
	// field 1 is the Resource that gets the attempt's attributes.
	holder bool
}

// The fields of opentelemetry-proto v1 (common, resource, trace, logs,
// metrics) that hold messages.
var (
	anyValue, arrayValue, kvList, keyValue = &schema{}, &schema{}, &schema{}, &schema{}
	resourceSchema                         = &schema{msgs: map[uint64]*schema{1: keyValue, 3: {}}}
	scopeSchema                            = &schema{msgs: map[uint64]*schema{3: keyValue}}

	traceRequest, logsRequest, metricsRequest *schema
)

func init() {
	anyValue.msgs = map[uint64]*schema{5: arrayValue, 6: kvList}
	arrayValue.msgs = map[uint64]*schema{1: anyValue}
	kvList.msgs = map[uint64]*schema{1: keyValue}
	keyValue.msgs = map[uint64]*schema{2: anyValue}

	span := &schema{msgs: map[uint64]*schema{
		9:  keyValue,
		11: {msgs: map[uint64]*schema{3: keyValue}}, // events
		13: {msgs: map[uint64]*schema{4: keyValue}}, // links
		15: {},                                      // status
	}}
	traceRequest = request(span)

	logRecord := &schema{msgs: map[uint64]*schema{5: anyValue, 6: keyValue}}
	logsRequest = request(logRecord)

	exemplar := &schema{msgs: map[uint64]*schema{7: keyValue}}
	number := &schema{msgs: map[uint64]*schema{7: keyValue, 5: exemplar}}
	histogram := &schema{msgs: map[uint64]*schema{9: keyValue, 8: exemplar}}
	exponential := &schema{msgs: map[uint64]*schema{1: keyValue, 11: exemplar, 8: {}, 9: {}}}
	summary := &schema{msgs: map[uint64]*schema{7: keyValue, 6: {}}}
	points := func(dp *schema) *schema { return &schema{msgs: map[uint64]*schema{1: dp}} }
	metric := &schema{msgs: map[uint64]*schema{
		5: points(number), 7: points(number), 9: points(histogram), 10: points(exponential), 11: points(summary),
		12: keyValue,
	}}
	metricsRequest = request(metric)
}

// request is an export request of item: resources, each with scopes,
// each with items.
func request(item *schema) *schema {
	scoped := &schema{msgs: map[uint64]*schema{1: scopeSchema, 2: item}}
	holder := &schema{msgs: map[uint64]*schema{1: resourceSchema, 2: scoped}, holder: true}
	return &schema{msgs: map[uint64]*schema{1: holder}}
}

func requestSchema(signal string) *schema {
	switch signal {
	case Traces:
		return traceRequest
	case Logs:
		return logsRequest
	}
	return metricsRequest
}

// maxDepth bounds the nesting of a request, whose AnyValues may nest.
const maxDepth = 64

const (
	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// pbField is one field of a protobuf message.
type pbField struct {
	num, typ uint64
	// raw is the field's encoding after its tag, for a field that is not
	// length-delimited; payload is a length-delimited field's content.
	raw, payload []byte
}

var errTruncated = errors.New("otlp: protobuf: truncated message")

// fields splits b into its fields.
func fields(b []byte) ([]pbField, error) {
	var out []pbField
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		if n <= 0 {
			return nil, errTruncated
		}
		b = b[n:]
		f := pbField{num: tag >> 3, typ: tag & 7}
		if f.num == 0 {
			return nil, errors.New("otlp: protobuf: field number 0")
		}
		switch f.typ {
		case wireVarint:
			_, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTruncated
			}
			f.raw, b = b[:n], b[n:]
		case wireFixed64, wireFixed32:
			size := 8
			if f.typ == wireFixed32 {
				size = 4
			}
			if len(b) < size {
				return nil, errTruncated
			}
			f.raw, b = b[:size], b[size:]
		case wireBytes:
			l, n := binary.Uvarint(b)
			if n <= 0 {
				return nil, errTruncated
			}
			rest := b[n:]
			size := len(rest)
			if l < uint64(size) {
				size = int(l) // #nosec G115 -- below len(rest), an int
			} else if l > uint64(size) {
				return nil, errTruncated
			}
			f.payload, b = rest[:size], rest[size:]
		default:
			return nil, fmt.Errorf("otlp: protobuf: wire type %d is not used by OTLP", f.typ)
		}
		out = append(out, f)
	}
	return out, nil
}

func appendTag(out []byte, num, typ uint64) []byte { return binary.AppendUvarint(out, num<<3|typ) }

func appendLen(out []byte, num uint64, payload []byte) []byte {
	out = appendTag(out, num, wireBytes)
	out = binary.AppendUvarint(out, uint64(len(payload)))
	return append(out, payload...)
}

// encodeAttr is a KeyValue with a string AnyValue.
func encodeAttr(a Attr) []byte {
	value := appendLen(nil, 1, []byte(a.Value))
	return appendLen(appendLen(nil, 1, []byte(a.Key)), 2, value)
}

type rewriter struct {
	attrs []Attr
	mask  func(string) string
}

func rewriteProto(s *schema, body []byte, attrs []Attr, mask func(string) string) ([]byte, error) {
	r := rewriter{attrs: attrs, mask: mask}
	return r.message(s, body, 0)
}

func (r rewriter) message(s *schema, b []byte, depth int) ([]byte, error) {
	if depth > maxDepth {
		return nil, fmt.Errorf("otlp: protobuf: nested deeper than %d", maxDepth)
	}
	fs, err := fields(b)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(b)+64)
	sawResource := false
	for _, f := range fs {
		if f.typ != wireBytes {
			out = append(appendTag(out, f.num, f.typ), f.raw...)
			continue
		}
		sub := s.msgs[f.num]
		var payload []byte
		switch {
		case s.holder && f.num == 1:
			sawResource = true
			payload, err = r.resource(f.payload, depth+1)
		case sub != nil:
			payload, err = r.message(sub, f.payload, depth+1)
		default:
			payload = []byte(r.mask(string(f.payload)))
		}
		if err != nil {
			return nil, err
		}
		out = appendLen(out, f.num, payload)
	}
	if s.holder && !sawResource && len(r.attrs) > 0 {
		payload, err := r.resource(nil, depth+1)
		if err != nil {
			return nil, err
		}
		out = appendLen(out, 1, payload)
	}
	return out, nil
}

// resource rewrites a Resource: it drops the attributes the rewriter
// sets, masks the rest, and appends the rewriter's.
func (r rewriter) resource(b []byte, depth int) ([]byte, error) {
	fs, err := fields(b)
	if err != nil {
		return nil, err
	}
	var kept []byte
	for _, f := range fs {
		if f.num == 1 && f.typ == wireBytes {
			if key, _ := attrKey(f.payload); stamped(r.attrs, key) {
				continue
			}
			kept = appendLen(kept, f.num, f.payload)
			continue
		}
		if f.typ == wireBytes {
			kept = appendLen(kept, f.num, f.payload)
		} else {
			kept = append(appendTag(kept, f.num, f.typ), f.raw...)
		}
	}
	out, err := r.message(resourceSchema, kept, depth)
	if err != nil {
		return nil, err
	}
	for _, a := range r.attrs {
		out = appendLen(out, 1, encodeAttr(a))
	}
	return out, nil
}

// attrKey is a KeyValue's key.
func attrKey(kv []byte) (string, error) {
	fs, err := fields(kv)
	if err != nil {
		return "", err
	}
	key := ""
	for _, f := range fs {
		if f.num == 1 && f.typ == wireBytes {
			key = string(f.payload)
		}
	}
	return key, nil
}

// attrString is a KeyValue's key and its value when it is a string.
func attrString(kv []byte) (string, string, bool) {
	fs, err := fields(kv)
	if err != nil {
		return "", "", false
	}
	key, value, ok := "", "", false
	for _, f := range fs {
		switch {
		case f.num == 1 && f.typ == wireBytes:
			key = string(f.payload)
		case f.num == 2 && f.typ == wireBytes:
			vs, err := fields(f.payload)
			if err != nil {
				return "", "", false
			}
			for _, v := range vs {
				if v.num == 1 && v.typ == wireBytes {
					value, ok = string(v.payload), true
				}
			}
		}
	}
	return key, value, ok
}

func inspectProto(s *schema, body []byte) ([]Resource, error) {
	fs, err := fields(body)
	if err != nil {
		return nil, err
	}
	holder := s.msgs[1]
	var out []Resource
	for _, f := range fs {
		if f.num != 1 || f.typ != wireBytes {
			continue
		}
		r := Resource{Attributes: map[string]string{}}
		if err := protoStrings(holder, f.payload, &r.Strings, 0); err != nil {
			return nil, err
		}
		hs, err := fields(f.payload)
		if err != nil {
			return nil, err
		}
		for _, h := range hs {
			if h.num != 1 || h.typ != wireBytes {
				continue
			}
			rs, err := fields(h.payload)
			if err != nil {
				return nil, err
			}
			for _, a := range rs {
				if a.num != 1 || a.typ != wireBytes {
					continue
				}
				if k, v, ok := attrString(a.payload); ok {
					r.Attributes[k] = v
				}
			}
		}
		out = append(out, r)
	}
	return out, nil
}

func protoStrings(s *schema, b []byte, out *[]string, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("otlp: protobuf: nested deeper than %d", maxDepth)
	}
	fs, err := fields(b)
	if err != nil {
		return err
	}
	for _, f := range fs {
		if f.typ != wireBytes {
			continue
		}
		if sub := s.msgs[f.num]; sub != nil {
			if err := protoStrings(sub, f.payload, out, depth+1); err != nil {
				return err
			}
			continue
		}
		*out = append(*out, string(f.payload))
	}
	return nil
}
