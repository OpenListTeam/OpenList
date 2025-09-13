//go:build !windows

package local

import (
	"io/fs"
	"strings"
	"syscall"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
