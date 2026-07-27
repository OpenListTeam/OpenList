package fs

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
)

type pathTestDriver struct {
	driver.Driver
	storage model.Storage
}

func (d *pathTestDriver) GetStorage() *model.Storage { return &d.storage }

type pathTestFile struct {
	model.FileStreamer
	name string
}

func (f *pathTestFile) GetName() string { return f.name }

func TestTaskDataGetPaths(t *testing.T) {
	td := &TaskData{
		SrcActualPath: "src/file",
		DstActualPath: "dst/dir",
		SrcStorageMp:  "/srcMount",
		DstStorageMp:  "/dstMount",
	}
	if got := td.GetSrcPath(); got != "/srcMount/src/file" {
		t.Fatalf("GetSrcPath = %q", got)
	}
	if got := td.GetDstPath(); got != "/dstMount/dst/dir" {
		t.Fatalf("GetDstPath = %q", got)
	}

	localTemp := &TaskData{
		SrcActualPath: "/tmp/local-file",
		DstActualPath: "dst",
		DstStorageMp:  "/cloud",
	}
	if localTemp.GetSrcPath() != "" {
		t.Fatalf("local temp src must be empty for matching, got %q", localTemp.GetSrcPath())
	}
	if localTemp.GetDstPath() != "/cloud/dst" {
		t.Fatalf("GetDstPath = %q", localTemp.GetDstPath())
	}
}

func TestArchiveContentUploadTaskGetPaths(t *testing.T) {
	t1 := &ArchiveContentUploadTask{
		ObjName:       "report.txt",
		InPlace:       false,
		DstActualPath: "out",
		DstStorageMp:  "/mount",
	}
	if t1.GetSrcPath() != "" {
		t.Fatal("src should be empty")
	}
	if t1.GetDstPath() != "/mount/out/report.txt" {
		t.Fatalf("dst = %q", t1.GetDstPath())
	}
	if !task.MatchTaskPath(t1.GetSrcPath(), t1.GetDstPath(), "/mount/out/report.txt") {
		t.Fatal("exact archive upload destination file must match")
	}

	inPlace := &ArchiveContentUploadTask{
		ObjName:       "expanded-directory",
		InPlace:       true,
		DstActualPath: "out",
		DstStorageMp:  "/mount",
	}
	if inPlace.GetDstPath() != "/mount/out" {
		t.Fatalf("in-place directory task dst = %q", inPlace.GetDstPath())
	}
}

func TestUploadTaskGetPaths(t *testing.T) {
	t1 := &UploadTask{
		storage:          &pathTestDriver{storage: model.Storage{MountPath: "/mount"}},
		dstDirActualPath: "uploads",
		file:             &pathTestFile{name: "photo.jpg"},
	}
	if t1.GetDstPath() != "/mount/uploads/photo.jpg" {
		t.Fatalf("dst = %q", t1.GetDstPath())
	}
	if !task.MatchTaskPath(t1.GetSrcPath(), t1.GetDstPath(), "/mount/uploads/photo.jpg") {
		t.Fatal("exact upload destination file must match")
	}

	nilTask := &UploadTask{}
	if nilTask.GetSrcPath() != "" || nilTask.GetDstPath() != "" {
		t.Fatal("nil storage upload task paths should be empty")
	}
}
