// Package secret holds a job token in memory so that no formatting,
// logging or encoding path prints it.
package secret

import "log/slog"

const mask = "[redacted]"

// Token is a job token. Only [Token.Reveal] returns its value; every
// other way of printing it prints a mask.
type Token struct{ value string }

// New wraps value.
func New(value string) Token { return Token{value: value} }

// Reveal returns the token, for the one header that carries it.
func (t Token) Reveal() string { return t.value }

// Empty reports whether the token is empty.
func (t Token) Empty() bool { return t.value == "" }

// String implements fmt.Stringer with a mask.
func (t Token) String() string { return mask }

// GoString implements fmt.GoStringer with a mask.
func (t Token) GoString() string { return mask }

// LogValue implements slog.LogValuer with a mask.
func (t Token) LogValue() slog.Value { return slog.StringValue(mask) }

// MarshalText implements encoding.TextMarshaler with a mask.
func (t Token) MarshalText() ([]byte, error) { return []byte(mask), nil }

// UnmarshalText implements encoding.TextUnmarshaler, so an offer's
// token decodes straight into a Token.
func (t *Token) UnmarshalText(b []byte) error {
	t.value = string(b)
	return nil
}
