//go:build windows

package local

import (
	"io/fs"
	"path/filepath"
	"syscall"
)

func isHiddenOnWindows(f fs.FileInfo, fullPath string) bool {
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
