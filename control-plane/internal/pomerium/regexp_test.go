package pomerium

import (
	"regexp"
	"testing"
)

func mustRegexp(t *testing.T, s string) *regexp.Regexp {
	t.Helper()
	re, err := regexp.Compile(s)
	if err != nil {
		t.Fatal(err)
	}
	return re
}
