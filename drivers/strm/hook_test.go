package strm

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureLocalDirectories(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not supported on Windows")
	}

	root := t.TempDir()
	nested := filepath.Join(root, "library", "movie")
	if err := ensureLocalDirectories(nested, root, 0o755); err != nil {
		t.Fatalf("ensureLocalDirectories() error = %v", err)
	}

	for _, path := range []string{root, filepath.Join(root, "library"), nested} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if got := info.Mode().Perm(); got != 0o755 {
			t.Errorf("permissions for %q = %#o, want %#o", path, got, 0o755)
		}
	}

	if err := os.Chmod(filepath.Join(root, "library"), 0o700); err != nil {
		t.Fatalf("chmod existing directory: %v", err)
	}
	if err := ensureLocalDirectories(nested, root, 0o755); err != nil {
		t.Fatalf("ensureLocalDirectories() repairing existing directory error = %v", err)
	}
	info, err := os.Stat(filepath.Join(root, "library"))
	if err != nil {
		t.Fatalf("stat repaired directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Errorf("repaired directory permissions = %#o, want %#o", got, 0o755)
	}
}

func TestEnsureLocalDirectoriesRejectsPathOutsideBase(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := ensureLocalDirectories(outside, root, 0o755); err == nil {
		t.Fatal("ensureLocalDirectories() error = nil, want path validation error")
	}
}

func TestSetLocalStrmFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not supported on Windows")
	}

	path := filepath.Join(t.TempDir(), "movie.strm")
	if err := os.WriteFile(path, []byte("https://example.com/movie"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	driver := &Strm{Addition: Addition{SaveLocalPermMode: SaveLocalSharedPermMode}}
	setLocalStrmFilePermissions(driver, path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat test file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("file permissions = %#o, want %#o", got, 0o644)
	}
}
