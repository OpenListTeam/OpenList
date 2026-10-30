package driver

// LocalPathProvider is implemented by drivers whose objects live on the
// filesystem of the machine running OpenList.
//
// The S3 server uses it to keep a small per-object cache in extended
// attributes (content MD5 plus the size and modification time it belongs
// to). Drivers storing data elsewhere simply do not implement the
// interface, and every caller falls back to the generic code path.
type LocalPathProvider interface {
	// LocalPath maps actualPath, an internal storage path as returned by
	// internal/op, to a path on the local filesystem.
	LocalPath(actualPath string) string
}
