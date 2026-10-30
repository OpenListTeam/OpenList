package webdav

import (
	"testing"
)

func TestIsSameStorageSameDir(t *testing.T) {
	testCases := []struct {
		src, dstDir string
		want        bool
	}{
		{"/a/b/c.txt", "/a/b", true},
		{"/storage/data/main/file.tmp", "/storage/data/main", true},
		{"/dav/folder/sub/file.txt", "/dav/folder/sub", true},
		{"/file.txt", "/", true},
	}
	for _, tc := range testCases {
		if got := isSameStorage(tc.src, tc.dstDir); got != tc.want {
			t.Errorf("isSameStorage(%q, %q) = %v, want %v", tc.src, tc.dstDir, got, tc.want)
		}
	}
}
