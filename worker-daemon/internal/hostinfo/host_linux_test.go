package hostinfo

import "testing"

func TestParseMeminfo(t *testing.T) {
	got, err := parseMeminfo([]byte("MemTotal:       32000000 kB\nMemFree:  100 kB\nMemAvailable:   20971520 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 20*GiB {
		t.Errorf("got %d, want 20 GiB", got)
	}
	if _, err := parseMeminfo([]byte("MemTotal: 1 kB\n")); err == nil {
		t.Error("no MemAvailable should fail")
	}
}

func TestHostSnapshot(t *testing.T) {
	s, err := Host{}.Snapshot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.OS != "linux" || s.CPUs < 1 || s.MemAvailableGiB <= 0 || s.DiskAvailableGiB <= 0 {
		t.Errorf("implausible snapshot %+v", s)
	}
}
