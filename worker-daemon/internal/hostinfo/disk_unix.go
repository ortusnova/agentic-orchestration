//go:build unix

package hostinfo

import (
	"fmt"
	"syscall"
)

// diskAvailable returns the bytes available to an unprivileged writer
// on the file system holding path.
func diskAvailable(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("hostinfo: free disk at %s: %w", path, err)
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil //nolint:gosec,unconvert // the field types differ by OS; neither is negative
}
