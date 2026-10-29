package fs

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

func TestNamedCopyStageObjectPath(t *testing.T) {
	tests := []struct {
		name     string
		stageDir string
		srcPath  string
		dstName  string
		want     string
	}{
		{name: "source basename", stageDir: "/dst/.stage", srcPath: "/src/foo", want: "/dst/.stage/foo"},
		{name: "requested name", stageDir: "/dst/.stage", srcPath: "/src/foo", dstName: "bar", want: "/dst/.stage/bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := namedCopyStageObjectPath(tt.stageDir, tt.srcPath, tt.dstName); got != tt.want {
				t.Fatalf("namedCopyStageObjectPath(%q, %q, %q) = %q, want %q", tt.stageDir, tt.srcPath, tt.dstName, got, tt.want)
			}
		})
	}
}

func TestTransferCreatesNamedEmptyDirectory(t *testing.T) {
	previousCache := op.Cache
	op.Cache = op.NewCacheManager()
	t.Cleanup(func() { op.Cache = previousCache })
	sourceRoot, destinationRoot := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(sourceRoot, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	source := &local.Local{Storage: model.Storage{ID: 101, MountPath: "/source", Status: op.WORK}}
	source.RootFolderPath = sourceRoot
	destination := &local.Local{Storage: model.Storage{ID: 102, MountPath: "/destination", Status: op.WORK}}
	destination.RootFolderPath = destinationRoot
	for _, d := range []*local.Local{source, destination} {
		if err := d.Init(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	transferTask := &FileTransferTask{TaskType: copy, TaskData: TaskData{SrcStorage: source, DstStorage: destination, SrcActualPath: "/empty", DstActualPath: "/", DstName: "renamed"}}
	transferTask.SetCtx(context.Background())
	if err := transferTask.RunWithNextTaskCallback(func(*FileTransferTask) error { t.Fatal("empty directory scheduled a child"); return nil }); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(destinationRoot, "renamed"))
	if err != nil || !info.IsDir() {
		t.Fatalf("named empty directory missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destinationRoot, "empty")); !os.IsNotExist(err) {
		t.Fatalf("source name unexpectedly used: %v", err)
	}
}
