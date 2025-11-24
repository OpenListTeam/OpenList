//go:build windows || plan9 || netbsd || aix || illumos || solaris || js

package local

import "os"

func copyNamedPipe(_ string, _, _ os.FileMode) error {
	return nil
}
