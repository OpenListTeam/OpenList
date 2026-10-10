//go:build linux

package mem

func platformMemorySnapshot(host *MemorySnapshot, hostErr error, readFile memoryFileReader) (MemorySnapshot, error) {
	return cgroupAwareMemorySnapshot(host, hostErr, readFile)
}
