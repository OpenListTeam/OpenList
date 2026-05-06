package weiyun_open

import "testing"

func TestPickUploadedFilePrefersMatchingSizeAndNewerFile(t *testing.T) {
	files := []*File{
		{FileID: "older-match", FileName: "demo.txt", FileSize: 12, FileMTime: 10},
		{FileID: "size-miss", FileName: "demo.txt", FileSize: 99, FileMTime: 99},
		{FileID: "newer-match", FileName: "demo.txt", FileSize: 12, FileMTime: 20},
	}

	got, err := pickUploadedFile(files, 12)
	if err != nil {
		t.Fatalf("pick uploaded file: %v", err)
	}
	if got.FileID != "newer-match" {
		t.Fatalf("unexpected file picked: %s", got.FileID)
	}
}

func TestUploadCandidateNamesDeduplicatesFallback(t *testing.T) {
	names := uploadCandidateNames("demo.txt", "demo.txt")
	if len(names) != 1 || names[0] != "demo.txt" {
		t.Fatalf("unexpected names: %#v", names)
	}
}
