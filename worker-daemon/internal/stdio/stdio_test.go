package stdio_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/ortusnova/agentic-orchestration/worker-daemon/internal/stdio"
)

func TestFrameLayout(t *testing.T) {
	var b bytes.Buffer
	if err := stdio.WriteFrame(&b, stdio.Stdout, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if got, want := b.Bytes(), []byte{'O', 0, 0, 0, 2, 'h', 'i'}; !bytes.Equal(got, want) {
		t.Errorf("frame = %v, want %v", got, want)
	}
	if err := stdio.WriteFrame(&b, stdio.Stdout, make([]byte, stdio.MaxPayload+1)); !errors.Is(err, stdio.ErrTooLarge) {
		t.Errorf("a 1 MiB + 1 payload: err = %v, want ErrTooLarge", err)
	}
	head := []byte{'I', 0, 0, 0, 0}
	binary.BigEndian.PutUint32(head[1:], stdio.MaxPayload+1)
	if _, _, err := stdio.ReadFrame(bytes.NewReader(head)); !errors.Is(err, stdio.ErrTooLarge) {
		t.Errorf("reading a frame over 1 MiB: err = %v, want ErrTooLarge", err)
	}
	if _, _, err := stdio.ReadFrame(bytes.NewReader([]byte{'O', 0, 0, 0, 5, 'a'})); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("a cut frame: err = %v, want ErrUnexpectedEOF", err)
	}
}

func TestExitStatus(t *testing.T) {
	for _, code := range []int{0, 1, 255, -1} {
		p := stdio.ExitPayload(code)
		if len(p) != 4 {
			t.Fatalf("payload of %d bytes", len(p))
		}
		if got, err := stdio.ExitStatus(p); err != nil || got != code {
			t.Errorf("ExitStatus(ExitPayload(%d)) = %d, %v", code, got, err)
		}
	}
	if !bytes.Equal(stdio.ExitPayload(-1), []byte{0xff, 0xff, 0xff, 0xff}) {
		t.Error("-1 is not a big-endian signed integer")
	}
	if _, err := stdio.ExitStatus([]byte{1}); err == nil {
		t.Error("a one-byte exit payload was accepted")
	}
}

func TestDialAndAccept(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-Sortie-Worker-Protocol") != "1" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		c, err := stdio.Accept(w, r)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.WriteFrame(stdio.Stdin, []byte("in"))
		_ = c.WriteFrame(stdio.StdinClosed, nil)
		for {
			typ, p, err := c.ReadFrame()
			if err != nil {
				return
			}
			// Tell the test the type and size of what came.
			_ = c.WriteFrame(stdio.Stdin, append([]byte{typ}, strconv.Itoa(len(p))...))
			if typ == stdio.Exit {
				return
			}
		}
	}))
	defer srv.Close()

	h := http.Header{"Authorization": {"Bearer tok"}, "X-Sortie-Worker-Protocol": {"1"}}
	c, err := stdio.Dial(context.Background(), srv.URL+"/jobs/ONS-7/attempts/1", h, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	for _, want := range []struct {
		typ byte
		p   string
	}{{stdio.Stdin, "in"}, {stdio.StdinClosed, ""}} {
		typ, p, err := c.ReadFrame()
		if err != nil || typ != want.typ || string(p) != want.p {
			t.Fatalf("frame = %q %q %v, want %q %q", typ, p, err, want.typ, want.p)
		}
	}
	big := bytes.Repeat([]byte("x"), stdio.MaxPayload+10)
	if n, err := (stdio.Writer{C: c, Type: stdio.Stdout}).Write(big); err != nil || n != len(big) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := c.WriteFrame(stdio.Exit, stdio.ExitPayload(3)); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for {
		_, p, err := c.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		n, _ := strconv.Atoi(string(p[1:]))
		sizes = append(sizes, n)
		if p[0] == stdio.Exit {
			break
		}
	}
	if len(sizes) != 3 || sizes[0] != stdio.MaxPayload || sizes[1] != 10 || sizes[2] != 4 {
		t.Errorf("frame sizes = %v, want a 1 MiB frame, the 10 bytes left, then X", sizes)
	}

	if _, err := stdio.Dial(context.Background(), srv.URL+"/jobs/ONS-7/attempts/1", nil, nil); err == nil {
		t.Error("a refused upgrade dialled")
	} else if re := (*stdio.RefusedError)(nil); !errors.As(err, &re) || re.Status != http.StatusUnauthorized {
		t.Errorf("err = %v, want the 401 refusal", err)
	}
}

func TestAcceptNeedsTheUpgrade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = stdio.Accept(w, r)
	}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired || resp.Header.Get("Upgrade") != stdio.Protocol {
		t.Errorf("answer = %d, Upgrade %q; want 426 naming %s", resp.StatusCode, resp.Header.Get("Upgrade"), stdio.Protocol)
	}
}
