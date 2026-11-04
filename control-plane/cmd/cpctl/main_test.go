package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cpctl(t *testing.T, home string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := run(append([]string{"-home", home}, args...), &out)
	return out.String(), err
}

func TestIssueListRevoke(t *testing.T) {
	home := t.TempDir()
	out, err := cpctl(t, home, "init", "-public-url", "https://cp.example.org:8443")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "missing "+filepath.Join(home, "secrets", "cp-anthropic-token")) {
		t.Errorf("init does not name the secret files the operator writes:\n%s", out)
	}
	if _, err := cpctl(t, home, "secrets", "-missing"); err == nil {
		t.Error("secrets -missing succeeded with secret files missing")
	}

	out, err = cpctl(t, home, "worker", "issue", "taichi", "-flows", "control,egress")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"issued taichi", filepath.Join(home, "workers", "taichi", "worker.key"), `"url": "https://cp.example.org:8443"`, "cp.sh apply"} {
		if !strings.Contains(out, want) {
			t.Errorf("issue does not say %q:\n%s", want, out)
		}
	}
	out, err = cpctl(t, home, "worker", "list")
	if err != nil || !strings.Contains(out, "taichi") || !strings.Contains(out, "control,egress") {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if _, err := cpctl(t, home, "worker", "revoke", "taichi"); err != nil {
		t.Fatal(err)
	}
	out, _ = cpctl(t, home, "worker", "list")
	if strings.Contains(out, "taichi") {
		t.Errorf("taichi is listed after its revocation:\n%s", out)
	}
	cfg, _ := os.ReadFile(filepath.Join(home, "pomerium", "config.yaml"))
	if !strings.Contains(string(cfg), `"deny"`) {
		t.Errorf("with no worker listed every route should deny:\n%s", cfg)
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"worker"}, {"worker", "issue"}, {"init"}} {
		if _, err := cpctl(t, t.TempDir(), args...); err == nil {
			t.Errorf("cpctl %v succeeded", args)
		}
	}
}
