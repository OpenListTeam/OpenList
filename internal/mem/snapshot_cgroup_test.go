package mem

import (
	"errors"
	"os"
	"testing"
)

const (
	megabyte = uint64(1024 * 1024)
	gigabyte = uint64(1024 * 1024 * 1024)
)

type memoryFixture map[string]string

func (fixture memoryFixture) readFile(name string) ([]byte, error) {
	value, ok := fixture[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return []byte(value), nil
}

func TestPlatformMemorySnapshotCgroupV2(t *testing.T) {
	host := &MemorySnapshot{
		Limit:     128 * gigabyte,
		Used:      64 * gigabyte,
		Available: 64 * gigabyte,
		Source:    MemorySourceHost,
	}
	tests := []struct {
		name          string
		fixture       memoryFixture
		wantLimit     uint64
		wantAvailable uint64
		wantSource    string
	}{
		{
			name: "docker leaf limit",
			fixture: memoryFixture{
				procSelfCgroup:    "0::/docker/container-id\n",
				procSelfMountInfo: "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
				"/sys/fs/cgroup/docker/container-id/memory.max":     "4294967296\n",
				"/sys/fs/cgroup/docker/container-id/memory.current": "1073741824\n",
				"/sys/fs/cgroup/docker/memory.max":                  "max\n",
				"/sys/fs/cgroup/memory.max":                         "max\n",
			},
			wantLimit:     4 * gigabyte,
			wantAvailable: 3 * gigabyte,
			wantSource:    MemorySourceCgroupV2,
		},
		{
			name: "parent limit",
			fixture: memoryFixture{
				procSelfCgroup:    "0::/docker/container-id\n",
				procSelfMountInfo: "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
				"/sys/fs/cgroup/docker/container-id/memory.max": "max\n",
				"/sys/fs/cgroup/docker/memory.max":              "2147483648\n",
				"/sys/fs/cgroup/docker/memory.current":          "536870912\n",
				"/sys/fs/cgroup/memory.max":                     "max\n",
			},
			wantLimit:     2 * gigabyte,
			wantAvailable: 1536 * megabyte,
			wantSource:    MemorySourceCgroupV2,
		},
		{
			name: "parent has tighter available memory",
			fixture: memoryFixture{
				procSelfCgroup:    "0::/docker/container-id\n",
				procSelfMountInfo: "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
				"/sys/fs/cgroup/docker/container-id/memory.max":     "4294967296\n",
				"/sys/fs/cgroup/docker/container-id/memory.current": "1073741824\n",
				"/sys/fs/cgroup/docker/memory.max":                  "8589934592\n",
				"/sys/fs/cgroup/docker/memory.current":              "7516192768\n",
				"/sys/fs/cgroup/memory.max":                         "max\n",
			},
			wantLimit:     4 * gigabyte,
			wantAvailable: gigabyte,
			wantSource:    MemorySourceCgroupV2,
		},
		{
			name: "unlimited falls back to host",
			fixture: memoryFixture{
				procSelfCgroup:              "0::/\n",
				procSelfMountInfo:           "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
				"/sys/fs/cgroup/memory.max": "max\n",
			},
			wantLimit:     host.Limit,
			wantAvailable: host.Available,
			wantSource:    MemorySourceHost,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := cgroupAwareMemorySnapshot(host, nil, tt.fixture.readFile)
			if err != nil {
				t.Fatalf("cgroupAwareMemorySnapshot() error = %v", err)
			}
			if snapshot.Limit != tt.wantLimit || snapshot.Available != tt.wantAvailable || snapshot.Source != tt.wantSource {
				t.Fatalf("snapshot = %+v, want limit=%d available=%d source=%q", snapshot, tt.wantLimit, tt.wantAvailable, tt.wantSource)
			}
		})
	}
}

func TestPlatformMemorySnapshotCgroupV1(t *testing.T) {
	host := &MemorySnapshot{
		Limit:     64 * gigabyte,
		Used:      32 * gigabyte,
		Available: 32 * gigabyte,
		Source:    MemorySourceHost,
	}
	const unlimited = "9223372036854771712\n"
	tests := []struct {
		name          string
		leafLimit     string
		leafUsage     string
		wantLimit     uint64
		wantAvailable uint64
		wantSource    string
	}{
		{
			name:          "finite docker limit",
			leafLimit:     "2147483648\n",
			leafUsage:     "536870912\n",
			wantLimit:     2 * gigabyte,
			wantAvailable: 1536 * megabyte,
			wantSource:    MemorySourceCgroupV1,
		},
		{
			name:          "kernel unlimited value is capped by host",
			leafLimit:     unlimited,
			leafUsage:     "1073741824\n",
			wantLimit:     host.Limit,
			wantAvailable: host.Available,
			wantSource:    MemorySourceHost,
		},
		{
			name:          "usage above limit saturates available",
			leafLimit:     "1073741824\n",
			leafUsage:     "2147483648\n",
			wantLimit:     gigabyte,
			wantAvailable: 0,
			wantSource:    MemorySourceCgroupV1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := memoryFixture{
				procSelfCgroup:    "5:cpu,memory:/docker/container-id\n",
				procSelfMountInfo: "35 23 0:31 / /sys/fs/cgroup/memory rw,nosuid,nodev,noexec,relatime - cgroup cgroup rw,memory\n",
				"/sys/fs/cgroup/memory/docker/container-id/memory.limit_in_bytes": tt.leafLimit,
				"/sys/fs/cgroup/memory/docker/container-id/memory.usage_in_bytes": tt.leafUsage,
				"/sys/fs/cgroup/memory/docker/memory.limit_in_bytes":              unlimited,
				"/sys/fs/cgroup/memory/docker/memory.usage_in_bytes":              "1073741824\n",
				"/sys/fs/cgroup/memory/memory.limit_in_bytes":                     unlimited,
				"/sys/fs/cgroup/memory/memory.usage_in_bytes":                     "1073741824\n",
			}
			snapshot, err := cgroupAwareMemorySnapshot(host, nil, fixture.readFile)
			if err != nil {
				t.Fatalf("cgroupAwareMemorySnapshot() error = %v", err)
			}
			if snapshot.Limit != tt.wantLimit || snapshot.Available != tt.wantAvailable || snapshot.Source != tt.wantSource {
				t.Fatalf("snapshot = %+v, want limit=%d available=%d source=%q", snapshot, tt.wantLimit, tt.wantAvailable, tt.wantSource)
			}
		})
	}
}

func TestPlatformMemorySnapshotResolvesMountRoot(t *testing.T) {
	host := &MemorySnapshot{Limit: 16 * gigabyte, Available: 8 * gigabyte, Source: MemorySourceHost}
	fixture := memoryFixture{
		procSelfCgroup:                           "0::/tenant/workload\n",
		procSelfMountInfo:                        "29 23 0:26 /tenant /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
		"/sys/fs/cgroup/workload/memory.max":     "1073741824\n",
		"/sys/fs/cgroup/workload/memory.current": "268435456\n",
		"/sys/fs/cgroup/memory.max":              "max\n",
	}
	snapshot, err := cgroupAwareMemorySnapshot(host, nil, fixture.readFile)
	if err != nil {
		t.Fatalf("cgroupAwareMemorySnapshot() error = %v", err)
	}
	if snapshot.Limit != gigabyte || snapshot.Available != 768*megabyte || snapshot.Source != MemorySourceCgroupV2 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestPlatformMemorySnapshotFailsClosedOnCgroupReadError(t *testing.T) {
	host := &MemorySnapshot{Limit: 16 * gigabyte, Available: 8 * gigabyte, Source: MemorySourceHost}
	fixture := memoryFixture{
		procSelfCgroup:    "0::/docker/container-id\n",
		procSelfMountInfo: "29 23 0:26 / /sys/fs/cgroup rw,nosuid,nodev,noexec,relatime - cgroup2 cgroup rw\n",
	}
	snapshot, err := cgroupAwareMemorySnapshot(host, nil, fixture.readFile)
	if err == nil {
		t.Fatal("cgroupAwareMemorySnapshot() expected an error")
	}
	if snapshot != (MemorySnapshot{}) {
		t.Fatalf("snapshot = %+v, want zero value", snapshot)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want os.ErrNotExist", err)
	}
}

func TestPlatformMemorySnapshotFailsClosedWithoutCgroupMount(t *testing.T) {
	host := &MemorySnapshot{Limit: 16 * gigabyte, Available: 8 * gigabyte, Source: MemorySourceHost}
	fixture := memoryFixture{
		procSelfCgroup:    "0::/docker/container-id\n",
		procSelfMountInfo: "29 23 0:26 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw\n",
	}
	snapshot, err := cgroupAwareMemorySnapshot(host, nil, fixture.readFile)
	if err == nil {
		t.Fatal("cgroupAwareMemorySnapshot() expected an error")
	}
	if snapshot != (MemorySnapshot{}) {
		t.Fatalf("snapshot = %+v, want zero value", snapshot)
	}
}
