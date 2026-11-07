//go:build linux || darwin || freebsd || netbsd

package s3

import (
	"bytes"
	"crypto/md5"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestMD5CacheEncoding(t *testing.T) {
	sum := md5.Sum([]byte("content"))
	raw := encodeMD5Cache(sum[:], 7, 1700000000000000000)

	if hash, ok := decodeMD5Cache(raw, 7, 1700000000000000000); !ok || !bytes.Equal(hash, sum[:]) {
		t.Fatalf("round trip failed: %x, %v", hash, ok)
	}
	if _, ok := decodeMD5Cache(raw, 8, 1700000000000000000); ok {
		t.Fatal("accepted a cache entry with a different size")
	}
	if _, ok := decodeMD5Cache(raw, 7, 1700000000000000001); ok {
		t.Fatal("accepted a cache entry with a different mtime")
	}
	for _, bad := range [][]byte{nil, []byte("zz:1:1"), []byte("ab:1:1"), []byte("aa:1"), []byte("aa:1:1:1")} {
		if _, ok := decodeMD5Cache(bad, 1, 1); ok {
			t.Fatalf("accepted malformed entry %q", bad)
		}
	}
}

// A value must never be recorded against content it was not computed from.
func TestEncodeCurrentEntryRejectsStaleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "object")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("hello"))
	mtime := fi.ModTime().UnixNano()

	if _, ok := encodeCurrentEntry(fi, sum[:], fi.Size(), mtime); !ok {
		t.Fatal("rejected an entry that still describes the file")
	}
	if _, ok := encodeCurrentEntry(fi, sum[:], fi.Size()+1, mtime); ok {
		t.Fatal("accepted an entry whose size no longer matches")
	}
	if _, ok := encodeCurrentEntry(fi, sum[:], fi.Size(), mtime+1); ok {
		t.Fatal("accepted an entry whose mtime no longer matches")
	}
	if _, ok := encodeCurrentEntry(fi, []byte("short"), fi.Size(), mtime); ok {
		t.Fatal("accepted a hash that is not an MD5")
	}

	// Replacing the file with same-length content is detected by its mtime.
	if err := os.WriteFile(path, []byte("HELLO"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, time.Now(), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	fi2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := encodeCurrentEntry(fi2, sum[:], fi2.Size(), mtime); ok {
		t.Fatal("accepted a stale hash for a file that was replaced in place")
	}
}

func TestMD5CacheSurvivesExtendedAttribute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "object")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum([]byte("hello"))
	value, ok := encodeCurrentEntry(fi, sum[:], fi.Size(), fi.ModTime().UnixNano())
	if !ok {
		t.Fatal("entry was rejected for a freshly written file")
	}
	if err := unix.Setxattr(path, md5Attr, value, 0); err != nil {
		t.Skipf("extended attributes unsupported here: %v", err)
	}

	var buf [cacheValueSize]byte
	n, err := unix.Getxattr(path, md5Attr, buf[:])
	if err != nil || n != len(value) {
		t.Fatalf("Getxattr = %d, err = %v, want %d", n, err, len(value))
	}
	if hash, ok := decodeMD5Cache(buf[:n], fi.Size(), fi.ModTime().UnixNano()); !ok || !bytes.Equal(hash, sum[:]) {
		t.Fatal("cache did not survive the xattr round trip")
	}
}
