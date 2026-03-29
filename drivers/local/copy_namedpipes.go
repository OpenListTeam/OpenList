//go:build !windows && !plan9 && !netbsd && !aix && !illumos && !solaris && !js

package local

import (
	"os"
	"path/filepath"
	"syscall"
)

func copyNamedPipe(dstPath string, mode os.FileMode, dirMode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dstPath), dirMode); err != nil {
		return err
	}
	return syscall.Mkfifo(dstPath, uint32(mode))
}
