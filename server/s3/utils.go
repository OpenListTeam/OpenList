// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	stderrors "errors"
	"path"
	"sort"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/gofakes3"
	"gorm.io/gorm/clause"
)

type Bucket struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

func mapBackendError(err error) error {
	if stderrors.Is(err, errs.TemporaryCapacity) {
		return gofakes3.ErrSlowDown
	}
	return err
}

func s3KeyHash(bucket, key string) string {
	hash := sha256.Sum256([]byte(bucket + "\x00" + key))
	return hex.EncodeToString(hash[:])
}

func getS3Marker(bucket, key string) (*model.S3Marker, error) {
	if db.GetDb() == nil {
		return nil, nil
	}
	var markers []model.S3Marker
	if err := db.GetDb().Where("bucket = ? AND key_hash = ?", bucket, s3KeyHash(bucket, key)).Find(&markers).Error; err != nil {
		return nil, err
	}
	for i := range markers {
		if markers[i].Key == key {
			return &markers[i], nil
		}
	}
	return nil, nil
}

func listS3Markers(bucket string) ([]model.S3Marker, error) {
	if db.GetDb() == nil {
		return nil, nil
	}
	var markers []model.S3Marker
	if err := db.GetDb().Where("bucket = ?", bucket).Find(&markers).Error; err != nil {
		return nil, err
	}
	sort.Slice(markers, func(i, j int) bool { return markers[i].Key < markers[j].Key })
	return markers, nil
}

func saveS3Marker(marker *model.S3Marker) error {
	if db.GetDb() == nil {
		return errs.NotImplement
	}
	return db.GetDb().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key_hash"}},
		DoUpdates: clause.AssignmentColumns([]string{"key", "metadata", "modified"}),
	}).Create(marker).Error
}

func deleteS3Marker(bucket, key string) error {
	if db.GetDb() == nil {
		return nil
	}
	return db.GetDb().Where("bucket = ? AND key_hash = ? AND key = ?", bucket, s3KeyHash(bucket, key), key).
		Delete(&model.S3Marker{}).Error
}

func decodeS3MarkerMetadata(marker *model.S3Marker) map[string]string {
	metadata := make(map[string]string)
	if marker != nil && marker.Metadata != "" {
		_ = json.Unmarshal([]byte(marker.Metadata), &metadata)
	}
	if metadata == nil {
		metadata = make(map[string]string)
	}
	return metadata
}

func s3MarkerMetadata(marker *model.S3Marker, objectName string) map[string]string {
	metadata := map[string]string{
		"Content-Disposition": utils.GenerateContentDisposition(path.Base(strings.TrimSuffix(objectName, "/"))),
		"Content-Type":        utils.GetMimeType(objectName),
	}
	for key, value := range decodeS3MarkerMetadata(marker) {
		metadata[key] = value
	}
	if marker != nil {
		metadata["Last-Modified"] = marker.Modified.Format(timeFormat)
	}
	return metadata
}

func getAndParseBuckets() ([]Bucket, error) {
	var res []Bucket
	err := json.Unmarshal([]byte(setting.GetStr(conf.S3Buckets)), &res)
	return res, err
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

func getDirEntries(ctx context.Context, path string) ([]model.Obj, error) {
	meta, _ := op.GetNearestMeta(path)
	fi, err := fs.Get(context.WithValue(ctx, conf.MetaKey, meta), path, &fs.GetArgs{})
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errs.IsNotFoundError(err) {
		return nil, gofakes3.ErrNoSuchKey
	} else if err != nil {
		return nil, gofakes3.ErrNoSuchKey
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
	s3accesskeyid := setting.GetStr(conf.S3AccessKeyId)
	s3secretaccesskey := setting.GetStr(conf.S3SecretAccessKey)
	if s3accesskeyid == "" && s3secretaccesskey == "" {
		return nil
	}
	authList := make(map[string]string)
	authList[s3accesskeyid] = s3secretaccesskey
	return authList
}
