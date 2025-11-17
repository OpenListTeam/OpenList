//go:build !windows

package local

import (
	"errors"
	"io/fs"
	"strings"
	"syscall"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"golang.org/x/sys/unix"
)

func isHidden(f fs.FileInfo, _ string) bool {
	return strings.HasPrefix(f.Name(), ".")
}

func getDiskUsage(path string) (model.DiskUsage, error) {
	var stat syscall.Statfs_t
	err := syscall.Statfs(path, &stat)
	if err != nil {
		return model.DiskUsage{}, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	free := stat.Bfree * uint64(stat.Bsize)
	return model.DiskUsage{
		TotalSpace: total,
		FreeSpace:  free,
	}, nil
}

func isCrossDeviceError(err error) bool {
	return errors.Is(err, unix.EXDEV)
}
