package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Scenario: Version. An operator runs `worker-daemon version`; it
// prints the daemon's version and protocol 1.
func TestVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"version"}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "worker-daemon "+version+"\n") {
		t.Errorf("output %q does not carry the version %q", got, version)
	}
	if !strings.Contains(got, "worker API protocol 1\n") {
		t.Errorf("output %q does not name protocol 1", got)
	}
}

func TestCheckConfig(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.json")
	_ = os.WriteFile(good, []byte(`{"worker_id":"taichi","control_plane":{"url":"http://127.0.0.1:7690"},
		"reserve":{"mem_gib":16},"floor":{"disk_gib":50},"state_dir":"`+dir+`"}`), 0o600)
	var out, errOut bytes.Buffer
	if code := run([]string{"check", "-config", good}, &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"worker_id":"taichi","control_plane":{"url":"http://127.0.0.1:7690"},"state_dir":"/x"}`), 0o600)
	errOut.Reset()
	if code := run([]string{"check", "-config", bad}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "reserve.mem_gib") {
		t.Errorf("exit %d, %q; want 1 naming reserve.mem_gib", code, errOut.String())
	}
}
