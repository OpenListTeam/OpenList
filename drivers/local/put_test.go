package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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
