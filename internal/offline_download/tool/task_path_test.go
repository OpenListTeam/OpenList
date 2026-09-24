package tool

import "testing"

func TestDownloadTaskGetPaths(t *testing.T) {
	tk := &DownloadTask{DstDirPath: "/cloud/dl"}
	if tk.GetSrcPath() != "" {
		t.Fatal("download src should be empty")
	}
	if tk.GetDstPath() != "/cloud/dl" {
		t.Fatalf("dst = %q", tk.GetDstPath())
	}
}

func TestTransferTaskGetPaths(t *testing.T) {
	// TransferTask embeds fs.TaskData — local temp src excluded
	tk := &TransferTask{}
	tk.SrcActualPath = "/tmp/x"
	tk.DstActualPath = "dir"
	tk.DstStorageMp = "/dst"
	if tk.GetSrcPath() != "" {
		t.Fatalf("temp src must be empty, got %q", tk.GetSrcPath())
	}
	if tk.GetDstPath() != "/dst/dir" {
		t.Fatalf("dst = %q", tk.GetDstPath())
	}

	tk.SrcStorageMp = "/src"
	tk.SrcActualPath = "a/b"
	if tk.GetSrcPath() != "/src/a/b" {
		t.Fatalf("src = %q", tk.GetSrcPath())
	}
}
