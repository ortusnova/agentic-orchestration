package hostinfo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// memAvailable returns the free, inactive, speculative and purgeable
// pages vm_stat reports, in bytes: what macOS gives a new workload
// without paging others out.
func memAvailable() (uint64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/bin/vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("hostinfo: vm_stat: %w", err)
	}
	return parseVMStat(out)
}

var pageSizePattern = regexp.MustCompile(`page size of (\d+) bytes`)

func parseVMStat(b []byte) (uint64, error) {
	m := pageSizePattern.FindSubmatch(b)
	if m == nil {
		return 0, errors.New("hostinfo: vm_stat printed no page size")
	}
	page, err := strconv.ParseUint(string(m[1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("hostinfo: vm_stat page size: %w", err)
	}
	want := map[string]bool{"Pages free": true, "Pages inactive": true, "Pages speculative": true, "Pages purgeable": true}
	var pages uint64
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		name, value, ok := strings.Cut(sc.Text(), ":")
		if !ok || !want[strings.TrimSpace(name)] {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimSpace(value), "."), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("hostinfo: vm_stat %s: %w", name, err)
		}
		pages += n
	}
	return pages * page, nil
}

// hasKVM is false: macOS has no KVM; smolvm uses Hypervisor.framework.
func hasKVM() bool { return false }

// hasNested is false: Hypervisor.framework offers no nested guests to
// smolvm.
func hasNested() bool { return false }
