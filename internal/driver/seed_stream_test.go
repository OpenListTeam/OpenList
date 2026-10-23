package driver

import (
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestSeedHashStreamCarriesMetadataAndHashes(t *testing.T) {
	md5Hash := strings.Repeat("a", utils.MD5.Width)
	sha1Hash := strings.Repeat("b", utils.SHA1.Width)
	whole := utils.NewHashInfoByMap(map[*utils.HashType]string{
		utils.MD5:  md5Hash,
		utils.SHA1: sha1Hash,
	})
	stream := NewSeedHashStream(&SeedRapidUploadRequest{
		Name:  "hello.txt",
		Size:  5,
		Whole: &whole,
	})

	if got := stream.GetName(); got != "hello.txt" {
		t.Fatalf("GetName() = %q", got)
	}
	if got := stream.GetSize(); got != 5 {
		t.Fatalf("GetSize() = %d", got)
	}
	if got := stream.GetHash().GetHash(utils.MD5); got != md5Hash {
		t.Fatalf("MD5 = %q", got)
	}
	if got := stream.GetHash().GetHash(utils.SHA1); got != sha1Hash {
		t.Fatalf("SHA1 = %q", got)
	}
	if !stream.IsForceStreamUpload() {
		t.Fatal("hash-only streams must force stream upload")
	}
	if stream.NeedStore() {
		t.Fatal("hash-only streams must not be stored")
	}
}

func TestSeedHashStreamNeverExposesContent(t *testing.T) {
	whole := utils.NewHashInfo(utils.MD5, strings.Repeat("c", utils.MD5.Width))
	stream := NewSeedHashStream(&SeedRapidUploadRequest{
		Name:  "hash-only.bin",
		Size:  1,
		Whole: &whole,
	})

	if n, err := stream.Read(make([]byte, 8)); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("Read() = (%d, %v)", n, err)
	}
	if _, err := stream.RangeRead(http_range.Range{Start: 0, Length: 1}); !errors.Is(err, io.EOF) {
		t.Fatalf("RangeRead() error = %v", err)
	}
	if _, err := stream.CacheFullAndWriter(nil, nil); !errors.Is(err, io.EOF) {
		t.Fatalf("CacheFullAndWriter() error = %v", err)
	}
}

func TestSeedHashStreamKeepsLazyContentSource(t *testing.T) {
	called := false
	stream := NewSeedHashStream(&SeedRapidUploadRequest{
		Name:  "with-source.bin",
		Size:  8,
		Whole: &utils.HashInfo{},
		Open: func() (model.FileStreamer, error) {
			called = true
			return nil, errors.New("content unavailable")
		},
	})

	if stream.Source == nil {
		t.Fatal("Source must be preserved for drivers that need a proof hash")
	}
	if _, err := stream.Source(); err == nil {
		t.Fatal("expected the content source error to propagate")
	}
	if !called {
		t.Fatal("Source was not invoked")
	}
}
