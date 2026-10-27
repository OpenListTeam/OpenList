package webdav

import "testing"

func TestLockRootHref(t *testing.T) {
	testCases := []struct {
		prefix, basePath, root string
		isDir                  bool
		want                   string
	}{
		{"/dav", "/storage", "/storage/lock-probe", true, "/dav/lock-probe/"},
		{"/dav", "/storage", "/storage/a.txt", false, "/dav/a.txt"},
		{"/dav", "/", "/storage/a.txt", false, "/dav/storage/a.txt"},
		{"/dav", "/storage", "/storage", true, "/dav/"},
		{"", "/", "/", true, "/"},
		{"/dav", "/", "/new folder/文件.txt", false, "/dav/new%20folder/%E6%96%87%E4%BB%B6.txt"},
	}
	for _, tc := range testCases {
		got := lockRootHref(tc.prefix, tc.basePath, tc.root, tc.isDir)
		if got != tc.want {
			t.Errorf("lockRootHref(%q, %q, %q, %v) = %q, want %q", tc.prefix, tc.basePath, tc.root, tc.isDir, got, tc.want)
		}
	}
}
