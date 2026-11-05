package secret

import "testing"

func TestMasker(t *testing.T) {
	m := &Masker{}
	m.Add(New("abcdefgh12345678"))
	m.AddValue("Bearer zyxwvutsrq")
	m.AddValue("short")
	for in, want := range map[string]string{
		"x abcdefgh12345678 y":                 "x [redacted] y",
		"abcdefgh12345678abcdefgh12345678":     "[redacted]",
		"auth: Bearer zyxwvutsrq":              "auth: [redacted]",
		"token zyxwvutsrq alone":               "token [redacted] alone",
		"short stays":                          "short stays",
		"nothing here":                         "nothing here",
		"abcdefgh12345678 and zyxwvutsrq both": "[redacted] and [redacted] both",
	} {
		if got := m.Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}
