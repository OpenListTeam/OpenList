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

	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
	// delimiter is the grouping character from the request. The file system
	// only knows "/", so any other value has to be applied to the emitted keys
	// instead of to the walk.
	delimiter string
	prefix    string
	hasPrefix bool
	// forcedGroup is the common prefix the walk is currently collecting keys
	// for because the marker points inside it, and forcedEmitted records that
	// this prefix has already been reported on this page.
	forcedGroup   string
	forcedEmitted bool
	// reported holds the common prefixes already put on this page, so a group
	// that several keys collapse into counts once and cannot truncate the page
	// a second time after it is complete.
	reported map[string]bool
}

func newObjectPage(page gofakes3.ListBucketPage) *objectPage {
	maxKeys := page.MaxKeys
	if maxKeys < 0 || maxKeys > maxListPageKeys {
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
		// max-keys=0 asks for an empty page, not for "there is more": flagging
		// truncation without a next marker made clients that loop while
		// IsTruncated is set re-issue the same request forever.
		if p.maxKeys > 0 {
			p.result.IsTruncated = true
		}
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
	if p.reported[prefix] {
		return true
	}
	if p.count >= p.maxKeys {
		if p.maxKeys > 0 {
			p.result.IsTruncated = true
		}
		return false
	}
	if p.reported == nil {
		p.reported = make(map[string]bool, 1)
	}
	p.reported[prefix] = true
	p.result.AddPrefix(prefix)
	p.count++
	p.lastKey = prefix
	return true
}

// markerCutsIntoPrefix reports whether the marker points inside the subtree a
// common prefix stands for, e.g. marker "a/1.txt" for the prefix "a/". Every key
// under such a prefix sorts after the prefix itself, so the prefix cannot be
// compared with the marker: some of its keys may still be behind the marker and
// some may be beyond it.
func (p *objectPage) markerCutsIntoPrefix(subtreePrefix string) bool {
	return len(p.marker) > len(subtreePrefix) && strings.HasPrefix(p.marker, subtreePrefix)
}

// addForcedPrefix reports the prefix the walk descended into because the marker
// cut into it. key is the key that proved to lie beyond the marker: it is part
// of that prefix, and S3 reports it as the prefix even though the prefix itself
// does not sort after the marker, so the marker comparison addPrefix does on
// the prefix is skipped in favour of the one on the key. The prefix is reported
// at most once per page.
func (p *objectPage) addForcedPrefix(key string) bool {
	if key <= p.marker || p.forcedEmitted {
		return true
	}
	if p.count >= p.maxKeys {
		if p.maxKeys > 0 {
			p.result.IsTruncated = true
		}
		return false
	}
	p.forcedEmitted = true
	if p.reported == nil {
		p.reported = make(map[string]bool, 1)
	}
	p.reported[p.forcedGroup] = true
	p.result.AddPrefix(p.forcedGroup)
	p.count++
	p.lastKey = p.forcedGroup
	return true
}

// groupPrefix returns the common prefix a key belongs to under a delimiter that
// is not "/", and whether the key has to be grouped at all. S3 groups at the
// first delimiter *after* the request prefix, independently of the directory
// structure: with Delimiter=":" the key "c:d/e.txt" belongs to "c:", and
// "a:b" belongs to "a:".
func (p *objectPage) groupPrefix(key string) (string, bool) {
	if p.delimiter == "" || p.delimiter == "/" {
		return "", false
	}
	rest := key
	if p.hasPrefix && strings.HasPrefix(key, p.prefix) {
		rest = key[len(p.prefix):]
	}
	at := strings.Index(rest, p.delimiter)
	if at < 0 {
		return "", false
	}
	return key[:len(key)-len(rest)+at+len(p.delimiter)], true
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
	prefix *gofakes3.Prefix,
	page gofakes3.ListBucketPage,
) (*gofakes3.ObjectList, error) {
	result := newObjectPage(page)
	if prefix != nil {
		if prefix.HasDelimiter {
			result.delimiter = prefix.Delimiter
		}
		result.prefix = prefix.Prefix
		result.hasPrefix = prefix.HasPrefix
	}
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

	// fdPath is a relative walk position that may legitimately be "." for the
	// bucket root, so it is checked for containment instead of for dot segments:
	// a prefix that walks out of the bucket describes nothing, and an empty
	// listing is exactly that.
	fp := path.Join(bucket, fdPath)
	if !pathWithin(bucket, fp) {
		return true, nil
	}
	// A prefix that walks out of the bucket describes nothing; neither does one
	// that only gets there through a symbolic link pointing outside the mount.
	if err := confineToMount(fp); err != nil {
		return true, nil
	}
	dirEntries, err := b.listDir(ctx, fp)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	// workaround as s3 can't have empty files in directories, useful in deletions
	if len(dirEntries) == 0 {
		if page.forcedGroup != "" {
			// An empty directory inside a prefix the marker cut into is still
			// that prefix: there is no key to compare with the marker.
			return page.addForcedPrefix(page.forcedGroup), nil
		}
		// The placeholder exists so a folder-style listing can show an empty
		// directory. A recursive listing (no delimiter) has no such notion: AWS
		// lists only keys that exist, and emitting the placeholder there left one
		// phantom key per nesting level behind every `rm --recursive` / `purge`
		// pass, so those tools never converged.
		if page.delimiter != "/" || !strings.HasPrefix(emptyObjectName, name) {
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

	// Emit entries in the order their keys have in a flat S3 keyspace. A
	// directory sorts as if it carried its trailing slash, so "b.txt" comes
	// before "b/..." as it does in a real bucket. Without this a listing
	// resumed from a marker could silently skip or repeat keys.
	dirEntries = slices.Clone(dirEntries)
	sort.Slice(dirEntries, func(i, j int) bool {
		return listSortKey(dirEntries[i]) < listSortKey(dirEntries[j])
	})

	for _, entry := range dirEntries {
		object := entry.GetName()
		if strings.HasPrefix(object, s3TempKeyPrefix) {
			// An upload in flight streams into a temporary sibling; it is not a
			// key a client ever wrote, so it never shows up in a listing.
			continue
		}

		// workround for control-chars detect
		objectPath := path.Join(fdPath, object)

		if !strings.HasPrefix(object, name) {
			continue
		}

		if entry.IsDir() {
			// S3 common prefixes include the delimiter, e.g. "base/20261009T020008/".
			// Clients such as barman-cloud ignore prefixes without it. A
			// delimiter other than "/" cannot be decided from the directory
			// tree, so the walk descends and groups each key individually.
			subtreePrefix := objectPath + "/"
			if addPrefix && page.delimiter == "/" {
				// A marker inside this prefix leaves keys beyond the marker in
				// it, and they belong to the same common prefix: walk the
				// subtree and report the prefix from the keys that survive
				// instead of skipping it, which dropped every one of them.
				if page.markerCutsIntoPrefix(subtreePrefix) {
					saved := page.forcedGroup
					page.forcedGroup = subtreePrefix
					keepGoing, err := b.walkPage(ctx, bucket, objectPath, "", false, page)
					page.forcedGroup = saved
					if err != nil || !keepGoing {
						return keepGoing, err
					}
					continue
				}
				// response.AddPrefix(gofakes3.URLEncode(subtreePrefix))
				if !page.addPrefix(subtreePrefix) {
					return false, nil
				}
				continue
			}
			// A marker beyond this subtree lets us avoid an upstream directory read.
			if subtreePrefix <= page.marker && !strings.HasPrefix(page.marker, subtreePrefix) {
				continue
			}
			keepGoing, err := b.walkPage(ctx, bucket, objectPath, "", false, page)
			if err != nil || !keepGoing {
				return keepGoing, err
			}
		} else {
			if page.forcedGroup != "" {
				if !page.addForcedPrefix(objectPath) {
					return false, nil
				}
				continue
			}
			if grouped, ok := page.groupPrefix(objectPath); ok {
				if !page.addPrefix(grouped) {
					return false, nil
				}
				continue
			}
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

// listSortKey returns the name used to order directory entries so that the
// emitted keys follow S3 lexicographic order.
func listSortKey(entry model.Obj) string {
	if entry.IsDir() {
		return entry.GetName() + "/"
	}
	return entry.GetName()
}
