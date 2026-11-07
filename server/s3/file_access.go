package s3

import (
	"github.com/OpenListTeam/OpenList/v4/internal/access"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// recordFileRead notes that an object was read, in the table the hot/cold
// tiering task reads (/api/admin/file_access/list).
//
// The S3 gateway is how a containerised application reads the library, and until
// now it was the one reader that left no trace: file_access only saw the /d, /p
// and WebDAV paths, so a file served to an application looked untouched since
// the day it was written, and the tiering task was free to move it to cold
// storage while it was in active use.
//
// Two paths are recorded because an object reached through an alias mount has
// two names: the one the client asked for (a mount-visible path like
// /logto/default/x/index.html) and the one the file is listed under when the
// tiering task walks hot_dir (the path the alias points at). The second one is
// what a hot/cold split can act on, so it must not be dropped; the first is kept
// because it is the honest answer to "which path was read".
func recordFileRead(bucket, requested string, file model.Obj) {
	if requested != "" {
		access.RecordBucket(bucket, requested)
	}
	if file == nil {
		return
	}
	p := file.GetPath()
	if p == "" || p == requested {
		return
	}
	// A driver reports a path inside its own mount, and some of those are not
	// paths at all in the filesystem view (/default/x/index.html under a mount
	// rooted elsewhere). Recording them would fill the admin listing with rows
	// nobody can walk, so only a path that names a storage is recorded.
	if _, _, err := op.GetStorageAndActualPath(p); err != nil {
		return
	}
	access.RecordBucket(bucket, p)
}
