//go:build !linux

package mem

import "errors"

func platformMemorySnapshot(host *MemorySnapshot, hostErr error, _ memoryFileReader) (MemorySnapshot, error) {
	if host != nil {
		return *host, nil
	}
	if hostErr != nil {
		return MemorySnapshot{}, hostErr
	}
	return MemorySnapshot{}, errors.New("memory information is unavailable")
}
