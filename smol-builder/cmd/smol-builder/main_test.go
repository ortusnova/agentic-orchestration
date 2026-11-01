package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ortusnova/agentic-orchestration/smol-builder/internal/build"
)

type fakeBuilder struct {
	opts   build.Options
	called bool
	err    error
}

func (f *fakeBuilder) Build(_ context.Context, opts build.Options) (*build.Report, error) {
	f.called, f.opts = true, opts
	if f.err != nil {
		return nil, f.err
	}
	return &build.Report{Layers: []string{"base", "go"}, Image: "img", Pack: opts.Output, PackSHA256: "abc", PackBytes: 3}, nil
}

func TestRunPrintsOneJSONObject(t *testing.T) {
	var stdout, stderr bytes.Buffer
	b := &fakeBuilder{}
	code := run(context.Background(), []string{"build", "--repo", "r", "--output", "o.smolmachine", "--layers", "l"}, &stdout, &stderr, b)
	if code != 0 {
		t.Fatalf("exit %d, stderr %s", code, stderr.String())
	}
	if b.opts != (build.Options{Repo: "r", Output: "o.smolmachine", LayersDir: "l"}) {
		t.Fatalf("options %+v", b.opts)
	}
	dec := json.NewDecoder(&stdout)
	var report map[string]any
	if err := dec.Decode(&report); err != nil {
		t.Fatal(err)
	}
	if dec.More() {
		t.Fatal("stdout holds more than one JSON value")
	}
	if report["pack_sha256"] != "abc" || report["pack"] != "o.smolmachine" {
		t.Fatalf("report %v", report)
	}
}

func TestRunFailure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	b := &fakeBuilder{err: errors.New("layer rust: boom")}
	code := run(context.Background(), []string{"build", "--repo", "r", "--output", "o"}, &stdout, &stderr, b)
	if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "layer rust: boom") {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
	}
}

func TestRunUsage(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"pack"},
		{"build"},
		{"build", "--repo", "r"},
		{"build", "--output", "o"},
		{"build", "--repo", "r", "--output", "o", "extra"},
		{"build", "--bogus"},
	} {
		var stdout, stderr bytes.Buffer
		b := &fakeBuilder{}
		if code := run(context.Background(), args, &stdout, &stderr, b); code != 2 || b.called || stdout.Len() != 0 {
			t.Errorf("%v: exit %d, called %v, stdout %q", args, code, b.called, stdout.String())
		}
		if !strings.Contains(stderr.String(), "usage:") && !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Errorf("%v: stderr %q", args, stderr.String())
		}
	}
}
