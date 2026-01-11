//go:build windows

package local

import (
	"errors"
	"io/fs"
	"path/filepath"
	"syscall"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"golang.org/x/sys/windows"
)

func isHidden(f fs.FileInfo, fullPath string) bool {
	filePath := filepath.Join(fullPath, f.Name())
	namePtr, err := syscall.UTF16PtrFromString(filePath)
	if err != nil {
		return false
	}
	attrs, err := syscall.GetFileAttributes(namePtr)
	if err != nil {
		return false
	}
	return attrs&syscall.FILE_ATTRIBUTE_HIDDEN != 0
}

func getDiskUsage(path string) (model.DiskUsage, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return model.DiskUsage{}, err
	}
	root := filepath.VolumeName(abs)
	if len(root) != 2 || root[1] != ':' {
		return model.DiskUsage{}, errors.New("cannot get disk label")
	}
	var freeBytes, totalBytes, totalFreeBytes uint64
	err = windows.GetDiskFreeSpaceEx(
		windows.StringToUTF16Ptr(root),
		&freeBytes,
		&totalBytes,
		&totalFreeBytes,
	)
	if err != nil {
		return model.DiskUsage{}, err
	}
	return model.DiskUsage{
		TotalSpace: int64(totalBytes),
		UsedSpace:  int64(totalBytes - freeBytes),
	}, nil
}

func isCrossDeviceError(err error) bool {
	return errors.Is(err, windows.ERROR_NOT_SAME_DEVICE)
}
