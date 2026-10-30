package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

type blockedReader struct {
	started chan struct{}
	release chan struct{}
}

func (r blockedReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.release
	return 0, context.Canceled
}

func TestPutReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "test.bin")
	if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
		t.Fatal(err)
	}

	file := &stream.FileStream{
		Obj:    &model.Object{Name: "test.bin", Size: 3},
		Reader: bytes.NewReader([]byte("new")),
	}
	if err := (&Local{}).Put(context.Background(), &model.Object{Path: root}, file, nil); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Fatalf("file contents = %q, want %q", got, "new")
	}
}

func TestPutPreservesExistingFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not supported on Windows")
	}

	for _, test := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "0600", mode: 0o600},
		{name: "0755", mode: 0o755},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "test.bin")
			if err := os.WriteFile(path, []byte("old"), 0o666); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, test.mode); err != nil {
				t.Fatal(err)
			}

			file := &stream.FileStream{
				Obj:    &model.Object{Name: "test.bin", Size: 3},
				Reader: bytes.NewReader([]byte("new")),
			}
			if err := (&Local{}).Put(context.Background(), &model.Object{Path: root}, file, nil); err != nil {
				t.Fatalf("Put: %v", err)
			}

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != test.mode {
				t.Errorf("file mode = %o, want %o", got, test.mode)
			}
		})
	}
}

func TestPutWritesThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require extra privileges on Windows")
	}

	for _, test := range []struct {
		name     string
		existing bool
	}{
		{name: "existing target", existing: true},
		{name: "dangling link", existing: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target.bin")
			link := filepath.Join(root, "alias.bin")
			if test.existing {
				if err := os.WriteFile(target, []byte("old"), 0o666); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink("target.bin", link); err != nil {
				t.Fatal(err)
			}

			file := &stream.FileStream{
				Obj:    &model.Object{Name: "alias.bin", Size: 3},
				Reader: bytes.NewReader([]byte("new")),
			}
			if err := (&Local{}).Put(context.Background(), &model.Object{Path: root}, file, nil); err != nil {
				t.Fatalf("Put: %v", err)
			}

			info, err := os.Lstat(link)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("link was replaced by a regular file")
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "new" {
				t.Fatalf("target contents = %q, want %q", got, "new")
			}
		})
	}
}

func TestPutRejectsSymlinkLoop(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require extra privileges on Windows")
	}

	root := t.TempDir()
	if err := os.Symlink("b.bin", filepath.Join(root, "a.bin")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.bin", filepath.Join(root, "b.bin")); err != nil {
		t.Fatal(err)
	}

	file := &stream.FileStream{
		Obj:    &model.Object{Name: "a.bin", Size: 3},
		Reader: bytes.NewReader([]byte("new")),
	}
	if err := (&Local{}).Put(context.Background(), &model.Object{Path: root}, file, nil); err == nil {
		t.Fatal("Put succeeded on a symlink loop")
	}
	for _, name := range []string{"a.bin", "b.bin"} {
		info, err := os.Lstat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("%s was replaced by a regular file", name)
		}
	}
}

func TestPutTempFileUsesExistingPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not supported on Windows")
	}

	root := t.TempDir()
	path := filepath.Join(root, "test.bin")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := blockedReader{started: make(chan struct{}), release: make(chan struct{})}
	file := &stream.FileStream{
		Obj:    &model.Object{Name: "test.bin", Size: 3},
		Reader: reader,
	}

	putDone := make(chan error, 1)
	go func() {
		putDone <- (&Local{}).Put(ctx, &model.Object{Path: root}, file, nil)
	}()
	<-reader.started

	temps, err := filepath.Glob(filepath.Join(root, ".openlist-upload-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 1 {
		t.Fatalf("temporary files = %v, want one", temps)
	}
	info, err := os.Stat(temps[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Errorf("temporary file mode = %o, want no group or other bits", got)
	}
	cancel()
	close(reader.release)
	if err := <-putDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want context.Canceled", err)
	}
}

func TestPutCancellationPreservesCompletedReplacement(t *testing.T) {
	root := t.TempDir()
	const content = "completed replacement"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := blockedReader{started: make(chan struct{}), release: make(chan struct{})}
	file := &stream.FileStream{
		Obj:    &model.Object{Name: "test.bin", Size: int64(len(content))},
		Reader: reader,
	}

	putDone := make(chan error, 1)
	go func() {
		putDone <- (&Local{}).Put(ctx, &model.Object{Path: root}, file, nil)
	}()
	<-reader.started

	path := filepath.Join(root, "test.bin")
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
	cancel()
	close(reader.release)
	if err := <-putDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Put error = %v, want context.Canceled", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("completed replacement was removed: %v", err)
	}
	if string(got) != content {
		t.Fatalf("replacement contents = %q, want %q", got, content)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "test.bin" {
		t.Fatalf("directory entries after cancellation = %v, want only test.bin", entries)
	}
}
