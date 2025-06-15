//go:build !windows

package local

import "io/fs"

func isHiddenOnWindows(_ fs.FileInfo, _ string) bool {
	return false
}
