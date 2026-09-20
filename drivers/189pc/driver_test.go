package _189pc

import (
	"path"
	"slices"
	"strings"
	"testing"
)

func TestFamilyTransferTempNamePreservesExtension(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "photo.jpg", want: ".jpg"},
		{name: "PHOTO.PNG", want: ".PNG"},
		{name: "archive.tar.gz", want: ".gz"},
		{name: "no-extension", want: ".transfer"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := familyTransferTempName(tt.name)
			if !strings.HasPrefix(got, "0") {
				t.Fatalf("temporary name %q must start with 0", got)
			}
			if ext := path.Ext(got); ext != tt.want {
				t.Fatalf("temporary name extension = %q, want %q", ext, tt.want)
			}
		})
	}
}

func TestUploadProgressKeysIncludeExtension(t *testing.T) {
	jpgKeys := uploadProgressKeys("session", "same-md5", "photo.jpg")
	txtKeys := uploadProgressKeys("session", "same-md5", "photo.txt")
	retryKeys := uploadProgressKeys("session", "same-md5", "different-name.jpg")

	if slices.Equal(jpgKeys, txtKeys) {
		t.Fatal("files with different extensions must not share upload progress")
	}
	if !slices.Equal(jpgKeys, retryKeys) {
		t.Fatal("files with the same session, MD5, and extension should share upload progress")
	}
}
