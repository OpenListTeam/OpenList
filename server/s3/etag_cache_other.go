//go:build !(linux || darwin || freebsd || netbsd)

package s3

// Extended attributes are not used on this platform; the S3 server falls
// back to hashing object content on every request, exactly as before.

func (b *s3Backend) readCachedHash(string) ([]byte, bool) { return nil, false }

func (b *s3Backend) storeCachedHash(string, []byte, int64, int64) {}
