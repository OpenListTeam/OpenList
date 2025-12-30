// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/itsHenry35/gofakes3"
	"github.com/ncw/swift/v2"
	log "github.com/sirupsen/logrus"
)

var (
	emptyPrefix = &gofakes3.Prefix{}
	timeFormat  = "Mon, 2 Jan 2006 15:04:05 GMT"
)

// s3Backend implements the gofacess3.Backend interface to make an S3
// backend for gofakes3
type s3Backend struct {
	meta *sync.Map
}

// newBackend creates a new SimpleBucketBackend.
func newBackend() gofakes3.Backend {
	return &s3Backend{
		meta: new(sync.Map),
	}
}

// ListBuckets always returns the default bucket.
func (b *s3Backend) ListBuckets(ctx context.Context) ([]gofakes3.BucketInfo, error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return nil, err
	}
	var response []gofakes3.BucketInfo
	for _, b := range buckets {
		node, _ := fs.Get(ctx, b.Path, &fs.GetArgs{})
		response = append(response, gofakes3.BucketInfo{
			// Name:         gofakes3.URLEncode(b.Name),
			Name:         b.Name,
			CreationDate: gofakes3.NewContentTime(node.ModTime()),
		})
	}
	return response, nil
}

// ListBucket lists the objects in the given bucket.
func (b *s3Backend) ListBucket(ctx context.Context, bucketName string, prefix *gofakes3.Prefix, page gofakes3.ListBucketPage) (*gofakes3.ObjectList, error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	if prefix == nil {
		prefix = emptyPrefix
	}

	// workaround
	if strings.TrimSpace(prefix.Prefix) == "" {
		prefix.HasPrefix = false
	}
	if strings.TrimSpace(prefix.Delimiter) == "" {
		prefix.HasDelimiter = false
	}

	response := gofakes3.NewObjectList()
	path, remaining := prefixParser(prefix)

	err = b.entryListR(bucketPath, path, remaining, prefix.HasDelimiter, response)
	if err == gofakes3.ErrNoSuchKey {
		// AWS just returns an empty list
		response = gofakes3.NewObjectList()
	} else if err != nil {
		return nil, err
	}

	return b.pager(response, page)
}

// HeadObject returns the fileinfo for the given object name.
//
// Note that the metadata is not supported yet.
func (b *s3Backend) HeadObject(ctx context.Context, bucketName, objectName string) (*gofakes3.Object, error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	fp := path.Join(bucketPath, objectName)
	fmeta, _ := op.GetNearestMeta(fp)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	if node.IsDir() {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	size := node.GetSize()
	// hash := getFileHashByte(fobj)

	meta := map[string]string{
		"Last-Modified": node.ModTime().Format(timeFormat),
		"Content-Type":  utils.GetMimeType(fp),
	}

	if val, ok := b.meta.Load(fp); ok {
		metaMap := val.(map[string]string)
		for k, v := range metaMap {
			meta[k] = v
		}
	}

	return &gofakes3.Object{
		Name: objectName,
		// Hash:     hash,
		Metadata: meta,
		Size:     size,
		Contents: noOpReadCloser{},
	}, nil
}

// GetObject fetchs the object from the filesystem.
func (b *s3Backend) GetObject(ctx context.Context, bucketName, objectName string, rangeRequest *gofakes3.ObjectRangeRequest) (s3Obj *gofakes3.Object, err error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return nil, err
	}
	bucketPath := bucket.Path

	fp := path.Join(bucketPath, objectName)
	fmeta, _ := op.GetNearestMeta(fp)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	if err != nil {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	if node.IsDir() {
		return nil, gofakes3.KeyNotFound(objectName)
	}

	link, file, err := fs.Link(ctx, fp, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if s3Obj == nil {
			_ = link.Close()
		}
	}()

	size := link.ContentLength
	if size <= 0 {
		size = file.GetSize()
	}
	rnge, err := rangeRequest.Range(size)
	if err != nil {
		return nil, err
	}

	rrf, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		return nil, fmt.Errorf("the remote storage driver need to be enhanced to support s3")
	}

	var rd io.Reader
	if rnge != nil {
		rd, err = rrf.RangeRead(ctx, http_range.Range(*rnge))
	} else {
		rd, err = rrf.RangeRead(ctx, http_range.Range{Length: -1})
	}
	if err != nil {
		return nil, err
	}

	meta := map[string]string{
		"Last-Modified":       node.ModTime().Format(timeFormat),
		"Content-Disposition": utils.GenerateContentDisposition(file.GetName()),
		"Content-Type":        utils.GetMimeType(fp),
	}

	if val, ok := b.meta.Load(fp); ok {
		metaMap := val.(map[string]string)
		for k, v := range metaMap {
			meta[k] = v
		}
	}

	return &gofakes3.Object{
		// Name: gofakes3.URLEncode(objectName),
		Name: objectName,
		// Hash:     "",
		Metadata: meta,
		Size:     size,
		Range:    rnge,
		Contents: utils.ReadCloser{Reader: rd, Closer: link},
	}, nil
}

// TouchObject creates or updates meta on specified object.
func (b *s3Backend) TouchObject(ctx context.Context, fp string, meta map[string]string) (result gofakes3.PutObjectResult, err error) {
	//TODO: implement
	return result, gofakes3.ErrNotImplemented
}

// PutObject creates or overwrites the object with the given name.
func (b *s3Backend) PutObject(
	ctx context.Context, bucketName, objectName string,
	meta map[string]string,
	input io.Reader, size int64,
) (result gofakes3.PutObjectResult, err error) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return result, err
	}
	bucketPath := bucket.Path

	isDir := strings.HasSuffix(objectName, "/")
	log.Debugf("isDir: %v", isDir)

	fp := path.Join(bucketPath, objectName)
	log.Debugf("fp: %s, bucketPath: %s, objectName: %s", fp, bucketPath, objectName)

	var reqPath string
	if isDir {
		reqPath = fp + "/"
	} else {
		reqPath = path.Dir(fp)
	}
	log.Debugf("reqPath: %s", reqPath)
	fmeta, _ := op.GetNearestMeta(fp)
	ctx = context.WithValue(ctx, conf.MetaKey, fmeta)

	_, err = fs.Get(ctx, reqPath, &fs.GetArgs{})
	if err != nil {
		if errs.IsObjectNotFound(err) && strings.Contains(objectName, "/") {
			log.Debugf("reqPath: %s not found and objectName contains /, need to makeDir", reqPath)
			err = fs.MakeDir(ctx, reqPath)
			if err != nil {
				return result, errors.WithMessagef(err, "failed to makeDir, reqPath: %s", reqPath)
			}
		} else {
			return result, gofakes3.KeyNotFound(objectName)
		}
	}

	if isDir {
		return result, nil
	}

	var ti time.Time

	if val, ok := meta["X-Amz-Meta-Mtime"]; ok {
		ti, _ = swift.FloatStringToTime(val)
	}

	if val, ok := meta["mtime"]; ok {
		ti, _ = swift.FloatStringToTime(val)
	}

	// If Modified is not set, use current time
	if ti.IsZero() {
		ti = time.Now()
	}

	obj := model.Object{
		Name:     path.Base(fp),
		Size:     size,
		Modified: ti,
		Ctime:    time.Now(),
	}
	// Check if system file should be ignored
	if setting.GetBool(conf.IgnoreSystemFiles) && utils.IsSystemFile(obj.Name) {
		return result, errs.IgnoredSystemFile
	}
	stream := &stream.FileStream{
		Obj:      &obj,
		Reader:   input,
		Mimetype: meta["Content-Type"],
	}

	err = fs.PutDirectly(ctx, reqPath, stream)
	if err != nil {
		return result, err
	}

	// if err := stream.Close(); err != nil {
	// 	// remove file when close error occurred (FsPutErr)
	// 	_ = fs.Remove(ctx, fp)
	// 	return result, err
	// }

	b.meta.Store(fp, meta)

	return result, nil
}

// DeleteMulti deletes multiple objects in a single request.
func (b *s3Backend) DeleteMulti(ctx context.Context, bucketName string, objects ...string) (result gofakes3.MultiDeleteResult, rerr error) {
	for _, object := range objects {
		if err := b.deleteObject(ctx, bucketName, object); err != nil {
			log.Errorf("delete object failed: %v", err)
			result.Error = append(result.Error, gofakes3.ErrorResult{
				Code:    gofakes3.ErrInternal,
				Message: gofakes3.ErrInternal.Message(),
				Key:     object,
			})
		} else {
			result.Deleted = append(result.Deleted, gofakes3.ObjectID{
				Key: object,
			})
		}
	}

	return result, nil
}

// DeleteObject deletes the object with the given name.
func (b *s3Backend) DeleteObject(ctx context.Context, bucketName, objectName string) (result gofakes3.ObjectDeleteResult, rerr error) {
	return result, b.deleteObject(ctx, bucketName, objectName)
}

// deleteObject deletes the object from the filesystem.
func (b *s3Backend) deleteObject(ctx context.Context, bucketName, objectName string) error {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return err
	}
	bucketPath := bucket.Path

	fp := path.Join(bucketPath, objectName)
	fmeta, _ := op.GetNearestMeta(fp)
	// S3 does not report an error when attemping to delete a key that does not exist, so
	// we need to skip IsNotExist errors.
	if _, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{}); err != nil && !errs.IsObjectNotFound(err) {
		return err
	}

	fs.Remove(ctx, fp)
	return nil
}

// CreateBucket creates a new bucket.
func (b *s3Backend) CreateBucket(ctx context.Context, name string) error {
	return gofakes3.ErrNotImplemented
}

// DeleteBucket deletes the bucket with the given name.
func (b *s3Backend) DeleteBucket(ctx context.Context, name string) error {
	return gofakes3.ErrNotImplemented
}

// BucketExists checks if the bucket exists.
func (b *s3Backend) BucketExists(ctx context.Context, name string) (exists bool, err error) {
	buckets, err := getAndParseBuckets()
	if err != nil {
		return false, err
	}
	for _, b := range buckets {
		if b.Name == name {
			return true, nil
		}
	}
	return false, nil
}

// CopyObject copy specified object from srcKey to dstKey.
func (b *s3Backend) CopyObject(ctx context.Context, srcBucket, srcKey, dstBucket, dstKey string, meta map[string]string) (result gofakes3.CopyObjectResult, err error) {
	if srcBucket == dstBucket && srcKey == dstKey {
		//TODO: update meta
		return result, nil
	}

	srcB, err := getBucketByName(srcBucket)
	if err != nil {
		return result, err
	}
	srcBucketPath := srcB.Path

	srcFp := path.Join(srcBucketPath, srcKey)
	fmeta, _ := op.GetNearestMeta(srcFp)
	srcNode, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), srcFp, &fs.GetArgs{})

	c, err := b.GetObject(ctx, srcBucket, srcKey, nil)
	if err != nil {
		return
	}
	defer func() {
		_ = c.Contents.Close()
	}()

	for k, v := range c.Metadata {
		if _, found := meta[k]; !found && k != "X-Amz-Acl" {
			meta[k] = v
		}
	}
	if _, ok := meta["mtime"]; !ok {
		meta["mtime"] = swift.TimeToFloatString(srcNode.ModTime())
	}

	_, err = b.PutObject(ctx, dstBucket, dstKey, meta, c.Contents, c.Size)
	if err != nil {
		return
	}

	return gofakes3.CopyObjectResult{
		ETag:         `"` + hex.EncodeToString(c.Hash) + `"`,
		LastModified: gofakes3.NewContentTime(srcNode.ModTime()),
	}, nil
}
