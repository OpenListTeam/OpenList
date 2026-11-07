//go:build linux || darwin || freebsd || netbsd

package s3

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// md5Attr caches "<content-md5-hex>:<mtime-unixnano>:<size>" on the object
// file. It is only ever a cache: an entry is used when the object's size and
// modification time still match, and ignored otherwise. The cached value is
// the content MD5, which is also the ETag of a single-part upload.
const md5Attr = "user.openlist.md5"

// cacheValueSize bounds the attribute value; an MD5, a nanosecond timestamp
// and a size never come close, and a longer value is not ours.
const cacheValueSize = 128

// localObjectPath maps a virtual object path to a file on this host. It only
// answers for storages that are unambiguous: when several storages are mounted
// at the same path, requests are load balanced and a path no longer identifies
// one file, so the cache stays out of the way.
func (b *s3Backend) localObjectPath(name string) (string, bool) {
	if op.StorageCountByPath(name) != 1 {
		return "", false
	}
	storage, actualPath, err := op.GetStorageAndActualPath(name)
	if err != nil {
		return "", false
	}
	provider, ok := storage.(driver.LocalPathProvider)
	if !ok {
		return "", false
	}
	return provider.LocalPath(actualPath), true
}

func encodeMD5Cache(hash []byte, size, mtimeNs int64) []byte {
	return []byte(fmt.Sprintf("%s:%d:%d", hex.EncodeToString(hash), mtimeNs, size))
}

// decodeMD5Cache returns the cached hash only when the entry describes the
// object as it is on disk now. File size and modification time alone cannot
// prove the content is unchanged, but the cache is only ever a shortcut: a
// miss falls back to hashing the content.
func decodeMD5Cache(raw []byte, size, mtimeNs int64) ([]byte, bool) {
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 {
		return nil, false
	}
	if parts[1] != strconv.FormatInt(mtimeNs, 10) || parts[2] != strconv.FormatInt(size, 10) {
		return nil, false
	}
	hash, err := hex.DecodeString(parts[0])
	if err != nil || len(hash) != md5.Size {
		return nil, false
	}
	return hash, true
}

// encodeCurrentEntry returns the attribute value for hash, but only when fi
// still describes the object the hash was computed from. Binding the value to
// the inspected file keeps a hash from being recorded next to content that
// another writer has replaced in the meantime.
func encodeCurrentEntry(fi os.FileInfo, hash []byte, size, mtimeNs int64) ([]byte, bool) {
	if len(hash) != md5.Size || fi.Size() != size || fi.ModTime().UnixNano() != mtimeNs {
		return nil, false
	}
	return encodeMD5Cache(hash, size, mtimeNs), true
}

// readCachedHash returns the cached content MD5 when it still matches the
// object on disk.
func (b *s3Backend) readCachedHash(name string) ([]byte, bool) {
	path, ok := b.localObjectPath(name)
	if !ok {
		return nil, false
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return nil, false
	}
	var buf [cacheValueSize]byte
	n, err := unix.Getxattr(path, md5Attr, buf[:])
	if err != nil || n <= 0 || n > len(buf) {
		return nil, false
	}
	return decodeMD5Cache(buf[:n], fi.Size(), fi.ModTime().UnixNano())
}

// storeCachedHash records hash for an object that was just written, but only
// while the file still has the size and modification time the hash belongs to.
// Filesystems without extended attribute support and objects that stopped
// matching between the write and this call are silently skipped: the cache is
// an optimisation, never a correctness requirement.
//
// The path is resolved rather than tested for being a symlink, because the
// write followed the link to the file that now holds the content: leaving the
// previous value there would let it outlive the content it describes, and
// readCachedHash resolves the same way.
func (b *s3Backend) storeCachedHash(name string, hash []byte, size, mtimeNs int64) {
	path, ok := b.localObjectPath(name)
	if !ok {
		return
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return
	}
	value, ok := encodeCurrentEntry(fi, hash, size, mtimeNs)
	if !ok {
		return
	}
	_ = unix.Setxattr(path, md5Attr, value, 0)
}
