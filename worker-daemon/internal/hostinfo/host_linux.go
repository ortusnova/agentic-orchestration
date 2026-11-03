package hostinfo

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// memInfo returns MemAvailable and MemTotal from /proc/meminfo, in
// bytes: the kernel's estimate of what a new workload can use without
// swapping, and the host's memory.
func memInfo() (available, total uint64, err error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, fmt.Errorf("hostinfo: %w", err)
	}
	return parseMeminfo(b)
}

func parseMeminfo(b []byte) (available, total uint64, err error) {
	got := map[string]uint64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 || (fields[0] != "MemAvailable:" && fields[0] != "MemTotal:") {
			continue
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("hostinfo: %s %q: %w", strings.TrimSuffix(fields[0], ":"), fields[1], err)
		}
		got[fields[0]] = kb * 1024
	}
	for _, k := range []string{"MemAvailable:", "MemTotal:"} {
		if _, ok := got[k]; !ok {
			return 0, 0, errors.New("hostinfo: /proc/meminfo has no " + strings.TrimSuffix(k, ":"))
		}
	}
	return got["MemAvailable:"], got["MemTotal:"], nil
}

// hasKVM reports whether this process can open /dev/kvm.
func hasKVM() bool {
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// hasNested reports whether the KVM module allows nested guests.
func hasNested() bool {
	if !hasKVM() {
		return false
	}
	for _, p := range []string{"/sys/module/kvm_intel/parameters/nested", "/sys/module/kvm_amd/parameters/nested"} {
		b, err := os.ReadFile(p) // #nosec G304 -- one of two fixed paths
		if err != nil {
			continue
		}
		switch strings.TrimSpace(string(b)) {
		case "Y", "y", "1":
			return true
		}
	}
	return false
}
