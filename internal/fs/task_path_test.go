package fs

import "testing"

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
		DstActualPath: "out",
		DstStorageMp:  "/mount",
	}
	if t1.GetSrcPath() != "" {
		t.Fatal("src should be empty")
	}
	if t1.GetDstPath() != "/mount/out" {
		t.Fatalf("dst = %q", t1.GetDstPath())
	}
}

func TestUploadTaskGetPathsNilStorage(t *testing.T) {
	t1 := &UploadTask{}
	if t1.GetSrcPath() != "" || t1.GetDstPath() != "" {
		t.Fatal("nil storage upload task paths should be empty")
	}
}
