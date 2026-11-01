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

// memAvailable returns MemAvailable from /proc/meminfo, in bytes: the
// kernel's estimate of what a new workload can use without swapping.
func memAvailable() (uint64, error) {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("hostinfo: %w", err)
	}
	return parseMeminfo(b)
}

func parseMeminfo(b []byte) (uint64, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("hostinfo: MemAvailable %q: %w", fields[1], err)
			}
			return kb * 1024, nil
		}
	}
	return 0, errors.New("hostinfo: /proc/meminfo has no MemAvailable")
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
