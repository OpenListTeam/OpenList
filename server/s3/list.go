// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"path"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/OpenListTeam/gofakes3"
	log "github.com/sirupsen/logrus"
)

// S3 ListObjects responses contain at most 1,000 keys.
const maxListPageKeys int64 = 1000

type objectPage struct {
	result  *gofakes3.ObjectList
	marker  string
	maxKeys int64
	count   int64
	lastKey string
}

func newObjectPage(page gofakes3.ListBucketPage) *objectPage {
	maxKeys := page.MaxKeys
	if maxKeys <= 0 || maxKeys > maxListPageKeys {
		maxKeys = maxListPageKeys
	}
	marker := ""
	if page.HasMarker {
		marker = page.Marker
	}
	return &objectPage{
		result:  gofakes3.NewObjectList(),
		marker:  marker,
		maxKeys: maxKeys,
	}
}

func (p *objectPage) addContent(item *gofakes3.Content) bool {
	if item.Key <= p.marker {
		return true
	}
	if p.count >= p.maxKeys {
		p.result.IsTruncated = true
		return false
	}
	p.result.Add(item)
	p.count++
	p.lastKey = item.Key
	return true
}

func (p *objectPage) addPrefix(prefix string) bool {
	if prefix <= p.marker {
		return true
	}
	if p.count >= p.maxKeys {
		p.result.IsTruncated = true
		return false
	}
	p.result.AddPrefix(prefix)
	p.count++
	p.lastKey = prefix
	return true
}

func (p *objectPage) finish() *gofakes3.ObjectList {
	if p.result.IsTruncated {
		p.result.NextMarker = p.lastKey
	}
	return p.result
}

func (b *s3Backend) listPage(
	ctx context.Context,
	bucket, fdPath, name string,
	addPrefix bool,
	page gofakes3.ListBucketPage,
) (*gofakes3.ObjectList, error) {
	result := newObjectPage(page)
	_, err := b.walkPage(ctx, bucket, fdPath, name, addPrefix, result)
	if err != nil {
		return nil, err
	}
	return result.finish(), nil
}

// walkPage returns false after finding an entry beyond the requested page.
func (b *s3Backend) walkPage(
	ctx context.Context,
	bucket, fdPath, name string,
	addPrefix bool,
	page *objectPage,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	fp := path.Join(bucket, fdPath)
	dirEntries, err := b.listDir(ctx, fp)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	// workaround as s3 can't have empty files in directories, useful in deletions
	if len(dirEntries) == 0 {
		if !strings.HasPrefix(emptyObjectName, name) {
			return true, nil
		}
		item := &gofakes3.Content{
			// Key:          gofakes3.URLEncode(path.Join(fdPath, emptyObjectName)),
			Key:          path.Join(fdPath, emptyObjectName),
			LastModified: gofakes3.NewContentTime(time.Now()),
			ETag:         getFileHash(nil), // No entry, so no hash
			Size:         0,
			StorageClass: gofakes3.StorageStandard,
		}
		log.Debugf("Adding empty object %s to response", item.Key)
		return page.addContent(item), nil
	}

	dirEntries = slices.Clone(dirEntries)
	sort.Slice(dirEntries, func(i, j int) bool {
		return dirEntries[i].GetName() < dirEntries[j].GetName()
	})

	for _, entry := range dirEntries {
		object := entry.GetName()

		// workround for control-chars detect
		objectPath := path.Join(fdPath, object)

		if !strings.HasPrefix(object, name) {
			continue
		}

		if entry.IsDir() {
			if addPrefix {
				// response.AddPrefix(gofakes3.URLEncode(objectPath))
				if !page.addPrefix(objectPath) {
					return false, nil
				}
				continue
			}
			subtreePrefix := objectPath + "/"
			// A marker beyond this subtree lets us avoid an upstream directory read.
			if subtreePrefix <= page.marker && !strings.HasPrefix(page.marker, subtreePrefix) {
				continue
			}
			keepGoing, err := b.walkPage(ctx, bucket, objectPath, "", false, page)
			if err != nil || !keepGoing {
				return keepGoing, err
			}
		} else {
			item := &gofakes3.Content{
				// Key:          gofakes3.URLEncode(objectPath),
				Key:          objectPath,
				LastModified: gofakes3.NewContentTime(entry.ModTime()),
				ETag:         getFileHash(entry),
				Size:         entry.GetSize(),
				StorageClass: gofakes3.StorageStandard,
			}
			if !page.addContent(item) {
				return false, nil
			}
		}
	}
	return true, nil
}
