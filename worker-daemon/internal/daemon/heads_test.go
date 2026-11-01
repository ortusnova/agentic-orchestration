package daemon

import "testing"

func TestParseHeads(t *testing.T) {
	const a, b = "aaaa", "bbbb"
	for name, tc := range map[string]struct {
		out, branch, tip string
	}{
		"issue branch at base":   {a + " HEAD\n" + a + " refs/heads/main\n" + a + " refs/heads/sortie/ONS-7\n", "sortie/ONS-7", a},
		"issue branch ahead":     {b + " HEAD\n" + a + " refs/heads/main\n" + b + " refs/heads/sortie/ONS-7\n", "sortie/ONS-7", b},
		"only branch, no HEAD":   {b + " refs/heads/feature\n", "feature", b},
		"head on another branch": {a + " HEAD\n" + a + " refs/heads/main\n" + b + " refs/heads/sortie/ONS-7\n", "main", a},
	} {
		t.Run(name, func(t *testing.T) {
			s, err := parseHeads(tc.out, "ONS-7")
			if err != nil {
				t.Fatal(err)
			}
			if s.Branch != tc.branch || s.Tip != tc.tip {
				t.Errorf("got %s at %s, want %s at %s", s.Branch, s.Tip, tc.branch, tc.tip)
			}
		})
	}
	if _, err := parseHeads(a+" HEAD\n"+a+" refs/heads/x\n"+a+" refs/heads/y\n", "ONS-7"); err == nil {
		t.Error("two branches at HEAD, neither named after the job: want an error")
	}
}
