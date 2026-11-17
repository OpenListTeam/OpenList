package weiyun_open

import "testing"

func TestBuildUploadRequestForEmptyFile(t *testing.T) {
	req, err := buildUploadRequest(nil, "empty.txt", 0, "parent-key")
	if err != nil {
		t.Fatalf("build upload request: %v", err)
	}
	if req.FileSHA != emptyFileSHA1 {
		t.Fatalf("unexpected file sha: %s", req.FileSHA)
	}
	if req.FileMD5 != emptyFileMD5 {
		t.Fatalf("unexpected file md5: %s", req.FileMD5)
	}
	if req.CheckSHA != emptySHA1StateHex {
		t.Fatalf("unexpected check sha: %s", req.CheckSHA)
	}
	if len(req.BlockSHAList) != 1 || req.BlockSHAList[0] != emptyFileSHA1 {
		t.Fatalf("unexpected block sha list: %#v", req.BlockSHAList)
	}
	if req.PdirKey != "parent-key" {
		t.Fatalf("unexpected parent key: %s", req.PdirKey)
	}
}
