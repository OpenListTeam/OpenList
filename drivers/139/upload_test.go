package _139

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestInitialUploadPartInfos(t *testing.T) {
	partInfos := make([]PartInfo, 150)
	for i := range partInfos {
		partInfos[i].PartNumber = int64(i + 1)
	}

	got := initialUploadPartInfos(partInfos)
	if len(got) != 100 {
		t.Fatalf("expected 100 part infos, got %d", len(got))
	}
	if got[0].PartNumber != 1 || got[len(got)-1].PartNumber != 100 {
		t.Fatalf("expected part numbers 1 through 100, got %d through %d", got[0].PartNumber, got[len(got)-1].PartNumber)
	}
}

func TestFindImageSuffixUploadDoesNotTreatExistingAsUploaded(t *testing.T) {
	oldFile := &model.Object{ID: "old-id", Name: "movie.mkv", Size: 1024}
	stream := &imageSuffixUploadStreamer{
		uploadName: "movie.mkv.jpg",
		finalName:  "movie.mkv",
		uploadedID: "new-id",
	}

	uploaded, existing, ambiguous := findImageSuffixUpload([]model.Obj{oldFile}, stream, 1024)
	if uploaded != nil {
		t.Fatalf("old file was incorrectly identified as uploaded file: %s", uploaded.GetID())
	}
	if ambiguous {
		t.Fatal("a single same-name file must not be reported as ambiguous")
	}
	if existing == nil || existing.GetID() != oldFile.GetID() {
		t.Fatal("existing original-name file was not identified for conflict handling")
	}
}

func TestFindImageSuffixUploadMatchesUploadedID(t *testing.T) {
	oldFile := &model.Object{ID: "old-id", Name: "movie.mkv", Size: 1024}
	uploadedFile := &model.Object{ID: "new-id", Name: "movie.mkv.jpg", Size: 1024}
	stream := &imageSuffixUploadStreamer{
		uploadName: "movie.mkv.jpg",
		finalName:  "movie.mkv",
		uploadedID: "new-id",
	}

	uploaded, existing, ambiguous := findImageSuffixUpload([]model.Obj{oldFile, uploadedFile}, stream, 1024)
	if ambiguous {
		t.Fatal("ID matching must not be reported as ambiguous")
	}
	if uploaded == nil || uploaded.GetID() != uploadedFile.GetID() {
		t.Fatal("did not select the current upload by ID")
	}
	if existing == nil || existing.GetID() != oldFile.GetID() {
		t.Fatal("did not keep the old same-name file separate")
	}
}

func TestFindImageSuffixUploadWithoutIDRequiresSameSize(t *testing.T) {
	stream := &imageSuffixUploadStreamer{
		uploadName: "movie.mkv.jpg",
		finalName:  "movie.mkv",
	}

	leftover := &model.Object{ID: "leftover-id", Name: "movie.mkv.jpg", Size: 8}
	uploaded, existing, ambiguous := findImageSuffixUpload([]model.Obj{leftover}, stream, 1024)
	if uploaded != nil {
		t.Fatal("a leftover .jpg with a different size was treated as this upload")
	}
	if existing != nil || ambiguous {
		t.Fatal("unexpected conflict or ambiguity for a size mismatch")
	}

	uploaded, _, ambiguous = findImageSuffixUpload([]model.Obj{leftover}, stream, 8)
	if uploaded == nil || uploaded.GetID() != leftover.GetID() {
		t.Fatal("a name and size match should be accepted when no file ID is available")
	}
	if ambiguous {
		t.Fatal("one name and size match must not be reported as ambiguous")
	}
}

func TestFindImageSuffixUploadWithoutIDReportsAmbiguity(t *testing.T) {
	stream := &imageSuffixUploadStreamer{
		uploadName: "movie.mkv.jpg",
		finalName:  "movie.mkv",
	}
	files := []model.Obj{
		&model.Object{ID: "leftover-id", Name: "movie.mkv.jpg", Size: 1024},
		&model.Object{ID: "other-id", Name: "movie.mkv.jpg", Size: 1024},
	}

	_, _, ambiguous := findImageSuffixUpload(files, stream, 1024)
	if !ambiguous {
		t.Fatal("two candidates without a file ID must be reported as ambiguous")
	}
}

func TestAddAppParallelHashContexts(t *testing.T) {
	data := make([]byte, 320)
	for i := range data {
		data[i] = byte(i % 251)
	}
	partInfos := []PartInfo{
		{PartNumber: 1, PartSize: 128},
		{PartNumber: 2, PartSize: 128},
		{PartNumber: 3, PartSize: 64},
	}

	if err := addAppParallelHashContexts(bytes.NewReader(data), partInfos); err != nil {
		t.Fatal(err)
	}
	if partInfos[0].ParallelHashCtx != nil {
		t.Fatal("first part should not include a parallel hash context")
	}

	want := [][]uint32{
		{0x593253ad, 0xfb4cc018, 0xbe611395, 0x485e47c1, 0x5a5b271d, 0xfb8da14f, 0xe8f77fb4, 0xd05eacbc},
		{0xedba51c8, 0xd14f2c6e, 0x30929803, 0x50ce3ed0, 0xc0270451, 0x7ebc882b, 0x7a17accc, 0x2ed273ad},
	}
	for i, offset := range []int64{128, 256} {
		ctx := partInfos[i+1].ParallelHashCtx
		if ctx == nil {
			t.Fatalf("part %d is missing its parallel hash context", i+2)
		}
		if ctx.PartOffset != offset {
			t.Errorf("part %d offset = %d, want %d", i+2, ctx.PartOffset, offset)
		}
		if !equalUint32s(ctx.H, want[i]) {
			t.Errorf("part %d midstate = %08x, want %08x", i+2, ctx.H, want[i])
		}
	}

	encoded, err := json.Marshal(partInfos[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("parallelHashCtx")) {
		t.Errorf("first part unexpectedly includes parallelHashCtx: %s", encoded)
	}
}

func TestAddAppParallelHashContextsRequiresAlignedOffsets(t *testing.T) {
	partInfos := []PartInfo{
		{PartNumber: 1, PartSize: 65},
		{PartNumber: 2, PartSize: 1},
	}
	if err := addAppParallelHashContexts(bytes.NewReader(make([]byte, 66)), partInfos); err == nil {
		t.Fatal("expected an error for a non-64-byte-aligned part offset")
	}
}

func equalUint32s(a, b []uint32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
