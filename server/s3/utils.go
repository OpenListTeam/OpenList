// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/gofakes3"
	log "github.com/sirupsen/logrus"
)

type Bucket struct {
	Name string `json:"name"`
	Path string `json:"path"`
	// Public marks the bucket as readable without AWS credentials (anonymous
	// GET/HEAD only). Writes always require a configured access key. It is
	// opt-in and off by default so existing deployments keep their current
	// access behaviour after upgrading.
	Public bool `json:"public,omitempty"`
}

func mapBackendError(err error) error {
	if stderrors.Is(err, errs.TemporaryCapacity) {
		return gofakes3.ErrSlowDown
	}
	return err
}

const emptyObjectName = "ThisIsAnEmptyFolderInTheS3Bucket"

var bucketsCache parsedSettingCache[[]Bucket]

// errBucketPathUnbounded is returned for a bucket whose path would expose the
// whole virtual filesystem instead of a subtree of it.
var errBucketPathUnbounded = stderrors.New("the path would expose every storage mount")

// cleanBucketPath normalises a bucket's configured path and rejects the shapes
// that turn one bucket into the root of the instance: an empty path, "/" and a
// path that climbs out with ".." all resolved to the mount root, and a bucket
// marked public in that shape served every storage anonymously.
func cleanBucketPath(name, bucketPath string) (string, error) {
	trimmed := strings.TrimSpace(bucketPath)
	if trimmed == "" {
		return "", stderrors.New("its path is empty")
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == ".." {
			return "", stderrors.New("its path contains a \"..\" segment")
		}
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(trimmed, "/"))
	if cleaned == "/" {
		return "", errBucketPathUnbounded
	}
	return cleaned, nil
}

func getAndParseBuckets() ([]Bucket, error) {
	raw := strings.TrimSpace(setting.GetStr(conf.S3Buckets))
	return bucketsCache.get(raw, func(raw string) ([]Bucket, error) {
		if raw == "" {
			return nil, nil
		}
		var parsed []Bucket
		if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
			log.Warnf("failed to parse %s as JSON: %v", conf.S3Buckets, err)
			return nil, err
		}
		kept := make([]Bucket, 0, len(parsed))
		for _, bucket := range parsed {
			cleaned, err := cleanBucketPath(bucket.Name, bucket.Path)
			if err != nil {
				log.Warnf("ignoring S3 bucket %q: %v", bucket.Name, err)
				continue
			}
			bucket.Path = cleaned
			kept = append(kept, bucket)
		}
		return kept, nil
	})
}

func getBucketByName(name string) (Bucket, error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return Bucket{}, err
	}
	for _, b := range buckets {
		if b.Name == name {
			return b, nil
		}
	}
	return Bucket{}, gofakes3.BucketNotFound(name)
}

// isPublicBucket reports whether the named bucket allows anonymous reads.
func isPublicBucket(name string) bool {
	bucket, err := getBucketByName(name)
	if err != nil {
		return false
	}
	return bucket.Public
}

func getDirEntries(ctx context.Context, path string) ([]model.Obj, error) {
	meta, _ := op.GetNearestMeta(path)
	fi, err := fs.Get(context.WithValue(ctx, conf.MetaKey, meta), path, &fs.GetArgs{})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		// Only a genuinely missing path means "empty listing". Folding every
		// other failure in here (expired upstream token, driver 5xx, rate limit)
		// answered 200 with zero keys, and a client that syncs with --delete
		// treats that as "the bucket is empty".
		if errs.IsNotFoundError(err) {
			return nil, gofakes3.ErrNoSuchKey
		}
		return nil, err
	}

	if !fi.IsDir() {
		return nil, gofakes3.ErrNoSuchKey
	}

	dirEntries, err := fs.List(context.WithValue(ctx, conf.MetaKey, meta), path, &fs.ListArgs{})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, err
	}

	return dirEntries, nil
}

// func getFileHashByte(node interface{}) []byte {
// 	b, err := hex.DecodeString(getFileHash(node))
// 	if err != nil {
// 		return nil
// 	}
// 	return b
// }

func getFileHash(node interface{}) string {
	// var o fs.Object

	// switch b := node.(type) {
	// case vfs.Node:
	// 	fsObj, ok := b.DirEntry().(fs.Object)
	// 	if !ok {
	// 		fs.Debugf("serve s3", "File uploading - reading hash from VFS cache")
	// 		in, err := b.Open(os.O_RDONLY)
	// 		if err != nil {
	// 			return ""
	// 		}
	// 		defer func() {
	// 			_ = in.Close()
	// 		}()
	// 		h, err := hash.NewMultiHasherTypes(hash.NewHashSet(hash.MD5))
	// 		if err != nil {
	// 			return ""
	// 		}
	// 		_, err = io.Copy(h, in)
	// 		if err != nil {
	// 			return ""
	// 		}
	// 		return h.Sums()[hash.MD5]
	// 	}
	// 	o = fsObj
	// case fs.Object:
	// 	o = b
	// }

	// hash, err := o.Hash(context.Background(), hash.MD5)
	// if err != nil {
	// 	return ""
	// }
	// return hash
	return ""
}

func prefixParser(p *gofakes3.Prefix) (path, remaining string) {
	idx := strings.LastIndexByte(p.Prefix, '/')
	if idx < 0 {
		return "", p.Prefix
	}
	return p.Prefix[:idx], p.Prefix[idx+1:]
}

// // FIXME this could be implemented by VFS.MkdirAll()
// func mkdirRecursive(path string, VFS *vfs.VFS) error {
// 	path = strings.Trim(path, "/")
// 	dirs := strings.Split(path, "/")
// 	dir := ""
// 	for _, d := range dirs {
// 		dir += "/" + d
// 		if _, err := VFS.Stat(dir); err != nil {
// 			err := VFS.Mkdir(dir, 0777)
// 			if err != nil {
// 				return err
// 			}
// 		}
// 	}
// 	return nil
// }

// func rmdirRecursive(p string, VFS *vfs.VFS) {
// 	dir := path.Dir(p)
// 	if !strings.ContainsAny(dir, "/\\") {
// 		// might be bucket(root)
// 		return
// 	}
// 	if _, err := VFS.Stat(dir); err == nil {
// 		err := VFS.Remove(dir)
// 		if err != nil {
// 			return
// 		}
// 		rmdirRecursive(dir, VFS)
// 	}
// }

func authlistResolver() map[string]string {
	return resolveAuthPairs()
}
