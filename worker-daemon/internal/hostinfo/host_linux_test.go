package hostinfo

import "testing"

func TestParseMeminfo(t *testing.T) {
	got, total, err := parseMeminfo([]byte("MemTotal:       33554432 kB\nMemFree:  100 kB\nMemAvailable:   20971520 kB\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 20*GiB || total != 32*GiB {
		t.Errorf("got %d of %d, want 20 GiB of 32 GiB", got, total)
	}
	if _, _, err := parseMeminfo([]byte("MemTotal: 1 kB\n")); err == nil {
		t.Error("no MemAvailable should fail")
	}
	if _, _, err := parseMeminfo([]byte("MemAvailable: 1 kB\n")); err == nil {
		t.Error("no MemTotal should fail")
	}
}

func TestHostSnapshot(t *testing.T) {
	s, err := Host{}.Snapshot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if s.OS != "linux" || s.CPUs < 1 || s.MemAvailableGiB <= 0 || s.MemTotalGiB < s.MemAvailableGiB || s.DiskAvailableGiB <= 0 {
		t.Errorf("implausible snapshot %+v", s)
	}
}
