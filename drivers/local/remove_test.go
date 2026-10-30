package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestRemoveDeletesThumbCache(t *testing.T) {
	root := t.TempDir()
	cacheDir := filepath.Join(root, "thumbs")
	if err := os.Mkdir(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(root, "photo.jpg")
	if err := os.WriteFile(filePath, []byte("image"), 0o666); err != nil {
		t.Fatal(err)
	}

	driver := &Local{
		Addition: Addition{
			ThumbCacheFolder: cacheDir,
		},
	}
	thumbPath := driver.thumbCachePath(filePath)
	if err := os.WriteFile(thumbPath, []byte("thumb"), 0o666); err != nil {
		t.Fatal(err)
	}

	err := driver.Remove(context.Background(), &model.Object{
		Path: filePath,
		Name: filepath.Base(filePath),
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err = os.Stat(thumbPath); !os.IsNotExist(err) {
		t.Fatalf("expected thumb cache to be removed, got %v", err)
	}
}

func TestCopyForMovePreservesRelativeSymlink(t *testing.T) {
	root := t.TempDir()
	srcPath := filepath.Join(root, "source")
	dstPath := filepath.Join(root, "recycle", "source")
	if err := os.Mkdir(srcPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcPath, "data"), []byte("data"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("data", filepath.Join(srcPath, "link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}

	driver := &Local{mkdirPerm: 0o755}
	if err := driver.copyForMove(srcPath, dstPath); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(srcPath); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(dstPath, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "data" {
		t.Fatalf("expected data through moved relative symlink, got %q", content)
	}
}

func TestCopyForMoveHandlesReadOnlyDirectory(t *testing.T) {
	root := t.TempDir()
	srcPath := filepath.Join(root, "source")
	dstPath := filepath.Join(root, "recycle", "source")
	if err := os.Mkdir(srcPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcPath, "data"), []byte("data"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(srcPath, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(srcPath, 0o755)

	driver := &Local{mkdirPerm: 0o755}
	if err := driver.copyForMove(srcPath, dstPath); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(dstPath, "data"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "data" {
		t.Fatalf("expected copied file content, got %q", content)
	}
	info, err := os.Stat(dstPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o555 {
		t.Fatalf("expected destination mode 0555, got %o", info.Mode().Perm())
	}
}

func TestCopyForMovePreservesModTime(t *testing.T) {
	root := t.TempDir()
	srcPath := filepath.Join(root, "source")
	dstPath := filepath.Join(root, "recycle", "source")
	filePath := filepath.Join(srcPath, "data")
	if err := os.Mkdir(srcPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("data"), 0o666); err != nil {
		t.Fatal(err)
	}
	fileTime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	dirTime := time.Date(2021, 6, 7, 8, 9, 10, 0, time.UTC)
	if err := os.Chtimes(filePath, fileTime, fileTime); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(srcPath, dirTime, dirTime); err != nil {
		t.Fatal(err)
	}

	driver := &Local{mkdirPerm: 0o755}
	if err := driver.copyForMove(srcPath, dstPath); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]time.Time{
		filepath.Join(dstPath, "data"): fileTime,
		dstPath:                        dirTime,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(want) {
			t.Fatalf("expected %s mod time %v, got %v", path, want, info.ModTime())
		}
	}
}
