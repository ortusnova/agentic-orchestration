package secret

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestTokenNeverPrints(t *testing.T) {
	var tok Token
	if err := json.Unmarshal([]byte(`"s3cret-value"`), &tok); err != nil {
		t.Fatal(err)
	}
	if tok.Reveal() != "s3cret-value" {
		t.Fatalf("Reveal = %q", tok.Reveal())
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("x", slog.Any("token", tok))
	b, _ := json.Marshal(struct{ T Token }{tok})
	for _, s := range []string{fmt.Sprint(tok), fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok), logs.String(), string(b)} {
		if strings.Contains(s, "s3cret") {
			t.Errorf("token printed: %s", s)
		}
	}
}
