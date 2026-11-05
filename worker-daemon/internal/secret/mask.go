package secret

import (
	"slices"
	"strings"
	"sync"
)

// MinMaskLen is the length in bytes of the shortest value a [Masker]
// masks, as the broker's masking: a shorter one would hide ordinary text.
const MinMaskLen = 8

// Masker replaces known values in text with the marker "[redacted]",
// by the broker's masking rules (acp-broker/telemetry-masking): a value
// of several lines is known line by line, a line with a space also by
// what follows its first run of spaces (the token of "Bearer <token>"),
// a value shorter than [MinMaskLen] is not masked, and occurrences that
// overlap or touch become one marker. It is safe for concurrent use.
type Masker struct {
	mu     sync.RWMutex
	values []string
}

// Add makes t's value known.
func (m *Masker) Add(t Token) { m.AddValue(t.Reveal()) }

// AddValue makes value known.
func (m *Masker) AddValue(value string) {
	var pieces []string
	for line := range strings.SplitSeq(value, "\n") {
		line = strings.TrimSuffix(line, "\r")
		pieces = append(pieces, line)
		if _, rest, ok := strings.Cut(line, " "); ok {
			if rest = strings.TrimLeft(rest, " "); rest != "" {
				pieces = append(pieces, rest)
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range pieces {
		if len(p) < MinMaskLen || strings.Contains(mask, p) || slices.Contains(m.values, p) {
			continue
		}
		m.values = append(m.values, p)
	}
}

// Mask replaces every occurrence of every known value in s with the
// marker.
func (m *Masker) Mask(s string) string {
	m.mu.RLock()
	vals := m.values
	m.mu.RUnlock()
	type span struct{ start, end int }
	var found []span
	for _, v := range vals {
		for from := 0; from <= len(s)-len(v); {
			i := strings.Index(s[from:], v)
			if i < 0 {
				break
			}
			found = append(found, span{from + i, from + i + len(v)})
			from += i + 1
		}
	}
	if len(found) == 0 {
		return s
	}
	slices.SortFunc(found, func(a, b span) int { return a.start - b.start })
	var b strings.Builder
	last, cur := 0, found[0]
	flush := func() {
		b.WriteString(s[last:cur.start])
		b.WriteString(mask)
		last = cur.end
	}
	for _, sp := range found[1:] {
		if sp.start <= cur.end {
			cur.end = max(cur.end, sp.end)
			continue
		}
		flush()
		cur = sp
	}
	flush()
	b.WriteString(s[last:])
	return b.String()
}
