// Package hostinfo reads what this host has: its OS and architecture,
// whether it offers KVM and nested virtualization, its CPUs, and the
// memory and disk available now.
package hostinfo

import (
	"runtime"
)

// GiB is a gibibyte in bytes.
const GiB = 1 << 30

// Snapshot is the host as read at one moment.
type Snapshot struct {
	OS     string
	Arch   string
	KVM    bool
	Nested bool
	CPUs   float64
	// MemAvailableGiB is the memory the host can give a new machine
	// without swapping.
	MemAvailableGiB float64
	// DiskAvailableGiB is the space an unprivileged writer has left on
	// the file system of the disk path.
	DiskAvailableGiB float64
}

// Probe reads a snapshot.
type Probe interface {
	Snapshot(diskPath string) (Snapshot, error)
}

// Host is the [Probe] of this host.
type Host struct{}

// Snapshot implements [Probe].
func (Host) Snapshot(diskPath string) (Snapshot, error) {
	mem, err := memAvailable()
	if err != nil {
		return Snapshot{}, err
	}
	disk, err := diskAvailable(diskPath)
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{
		OS: runtime.GOOS, Arch: runtime.GOARCH,
		KVM: hasKVM(), Nested: hasNested(),
		CPUs:            float64(runtime.NumCPU()),
		MemAvailableGiB: float64(mem) / GiB, DiskAvailableGiB: float64(disk) / GiB,
	}, nil
}
