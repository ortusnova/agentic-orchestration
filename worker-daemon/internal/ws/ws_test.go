package ws

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	gotAuth := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth <- r.Header.Get("Authorization")
		c, err := Accept(w, r)
		if err != nil {
			return
		}
		for {
			op, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(op, append([]byte("echo:"), msg...))
		}
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http")+"/stdio", http.Header{"Authorization": {"Bearer t"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a := <-gotAuth; a != "Bearer t" {
		t.Errorf("Authorization = %q", a)
	}
	big := bytes.Repeat([]byte("x"), 70000)
	for _, msg := range [][]byte{[]byte("hi"), bytes.Repeat([]byte("y"), 300), big} {
		if err := c.WriteMessage(OpBinary, msg); err != nil {
			t.Fatal(err)
		}
		op, got, err := c.ReadMessage()
		if err != nil || op != OpBinary || !bytes.Equal(got, append([]byte("echo:"), msg...)) {
			t.Fatalf("got %d %d bytes, %v", op, len(got), err)
		}
	}
	_ = c.Close()
	if _, _, err := c.ReadMessage(); err == nil {
		t.Error("read after close should fail")
	}
}

func TestRefusedHandshake(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil, nil)
	var he *HandshakeError
	if !errors.As(err, &he) || he.Status != http.StatusUnauthorized {
		t.Errorf("err = %v, want a 401 handshake error", err)
	}
}
