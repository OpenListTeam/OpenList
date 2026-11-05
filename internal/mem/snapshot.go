package mem

import (
	"os"

	gopsutilmem "github.com/shirou/gopsutil/v4/mem"
)

const (
	MemorySourceHost     = "host"
	MemorySourceCgroupV1 = "cgroup_v1"
	MemorySourceCgroupV2 = "cgroup_v2"
)

// MemorySnapshot describes the effective memory boundary visible to the
// current process. Available is always capped by Limit when Limit is known.
type MemorySnapshot struct {
	Limit     uint64
	Used      uint64
	Available uint64
	Source    string
}

type memoryFileReader func(string) ([]byte, error)

// GetMemorySnapshot returns a zero-value snapshot with any error so callers
// can fail closed instead of using incomplete memory information.
func GetMemorySnapshot() (MemorySnapshot, error) {
	virtualMemory, hostErr := gopsutilmem.VirtualMemory()
	var host *MemorySnapshot
	if virtualMemory != nil {
		available := min(virtualMemory.Available, virtualMemory.Total)
		host = &MemorySnapshot{
			Limit:     virtualMemory.Total,
			Used:      virtualMemory.Used,
			Available: available,
			Source:    MemorySourceHost,
		}
	}
	return platformMemorySnapshot(host, hostErr, os.ReadFile)
}

type cgroupMemory struct {
	limit     uint64
	available uint64
	source    string
}

func combineMemorySnapshots(host *MemorySnapshot, cgroup cgroupMemory) MemorySnapshot {
	if host == nil {
		available := min(cgroup.available, cgroup.limit)
		return MemorySnapshot{
			Limit:     cgroup.limit,
			Used:      saturatingSub(cgroup.limit, available),
			Available: available,
			Source:    cgroup.source,
		}
	}
	if cgroup.limit >= host.Limit && cgroup.available >= host.Available {
		return *host
	}
	limit := min(host.Limit, cgroup.limit)
	available := min(host.Available, cgroup.available, limit)
	return MemorySnapshot{
		Limit:     limit,
		Used:      saturatingSub(limit, available),
		Available: available,
		Source:    cgroup.source,
	}
}

func saturatingSub(left, right uint64) uint64 {
	if right >= left {
		return 0
	}
	return left - right
}
