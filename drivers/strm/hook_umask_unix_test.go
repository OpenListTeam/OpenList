//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package strm

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCreateLocalDirectoryRespectsUmask(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "library", "movie")

	originalUmask := syscall.Umask(0o027)
	defer syscall.Umask(originalUmask)

	if err := createLocalDirectory(nested); err != nil {
		t.Fatalf("createLocalDirectory() error = %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat created directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o750 {
		t.Errorf("created directory permissions = %#o, want %#o", got, 0o750)
	}
}
