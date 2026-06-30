package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
