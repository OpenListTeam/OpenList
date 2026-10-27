// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"path"
	"sort"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/gofakes3"
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

type listCandidate struct {
	key       string
	entry     model.Obj
	marker    *model.S3Marker
	directory bool
}

func markerCandidates(markers []model.S3Marker, fdPath, name string, includeExact bool) []listCandidate {
	candidates := make(map[string]listCandidate)
	add := func(candidate listCandidate) {
		if existing, ok := candidates[candidate.key]; ok {
			existing.directory = existing.directory || candidate.directory
			if candidate.marker != nil {
				existing.marker = candidate.marker
			}
			candidates[candidate.key] = existing
			return
		}
		candidates[candidate.key] = candidate
	}

	prefix := fdPath
	if prefix != "" {
		prefix += "/"
	}
	for i := range markers {
		marker := &markers[i]
		trimmed := strings.TrimSuffix(marker.Key, "/")
		if trimmed == "" {
			if marker.Key == "/" && fdPath == "" && name == "" {
				add(listCandidate{key: marker.Key, marker: marker})
			}
			continue
		}
		parent := ""
		child := trimmed
		if idx := strings.LastIndexByte(trimmed, '/'); idx >= 0 {
			parent = trimmed[:idx]
			child = trimmed[idx+1:]
		}
		if parent == fdPath && strings.HasPrefix(child, name) {
			add(listCandidate{key: marker.Key, marker: marker})
		}
		if includeExact && marker.Key == prefix && name == "" {
			add(listCandidate{key: marker.Key, marker: marker})
		}

		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		rest := strings.TrimPrefix(trimmed, prefix)
		if rest == "" {
			continue
		}
		child = rest
		if idx := strings.IndexByte(child, '/'); idx >= 0 {
			child = child[:idx]
		}
		if !strings.HasPrefix(child, name) {
			continue
		}
		virtualKey := prefix + child + "/"
		if virtualKey != marker.Key {
			add(listCandidate{key: virtualKey, directory: true})
		}
	}

	result := make([]listCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	return result
}

func (b *s3Backend) listPage(
	ctx context.Context,
	bucket, fdPath, name string,
	addPrefix bool,
	page gofakes3.ListBucketPage,
) (*gofakes3.ObjectList, error) {
	return b.listPageWithMarkers(ctx, bucket, fdPath, name, addPrefix, page, nil)
}

func (b *s3Backend) listPageWithMarkers(
	ctx context.Context,
	bucket, fdPath, name string,
	addPrefix bool,
	page gofakes3.ListBucketPage,
	markers []model.S3Marker,
) (*gofakes3.ObjectList, error) {
	result := newObjectPage(page)
	_, err := b.walkPage(ctx, bucket, fdPath, name, addPrefix, result, markers, true)
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
	markers []model.S3Marker,
	includeExactMarker bool,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}

	fp := path.Join(bucket, fdPath)
	dirEntries, err := b.listDir(ctx, fp)
	markerEntries := markerCandidates(markers, fdPath, name, includeExactMarker)
	if err != nil && len(markerEntries) == 0 {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}

	// Emit entries in the order their keys have in a flat S3 keyspace. A
	// directory sorts as if it carried its trailing slash, so "b.txt" comes
	// before "b/..." as it does in a real bucket.
	candidates := make([]listCandidate, 0, len(dirEntries)+len(markerEntries))
	for _, entry := range dirEntries {
		object := entry.GetName()
		if !strings.HasPrefix(object, name) {
			continue
		}
		objectPath := path.Join(fdPath, object)
		key := objectPath
		if entry.IsDir() {
			key += "/"
		}
		candidates = append(candidates, listCandidate{key: key, entry: entry, directory: entry.IsDir()})
	}
	candidates = append(candidates, markerEntries...)
	merged := make(map[string]listCandidate, len(candidates))
	for _, candidate := range candidates {
		if existing, ok := merged[candidate.key]; ok {
			existing.directory = existing.directory || candidate.directory
			if candidate.entry != nil {
				existing.entry = candidate.entry
			}
			if candidate.marker != nil {
				existing.marker = candidate.marker
			}
			merged[candidate.key] = existing
		} else {
			merged[candidate.key] = candidate
		}
	}
	candidates = candidates[:0]
	for _, candidate := range merged {
		candidates = append(candidates, candidate)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].key < candidates[j].key
	})

	for _, candidate := range candidates {
		if candidate.marker != nil {
			item := &gofakes3.Content{
				Key:          candidate.marker.Key,
				LastModified: gofakes3.NewContentTime(candidate.marker.Modified),
				ETag:         getFileHash(nil),
				Size:         0,
				StorageClass: gofakes3.StorageStandard,
			}
			if !page.addContent(item) {
				return false, nil
			}
			if !candidate.directory {
				continue
			}
		}

		if !candidate.directory {
			entry := candidate.entry
			item := &gofakes3.Content{
				Key:          candidate.key,
				LastModified: gofakes3.NewContentTime(entry.ModTime()),
				ETag:         getFileHash(entry),
				Size:         entry.GetSize(),
				StorageClass: gofakes3.StorageStandard,
			}
			if !page.addContent(item) {
				return false, nil
			}
			continue
		}

		if addPrefix {
			if !page.addPrefix(candidate.key) {
				return false, nil
			}
			continue
		}
		if page.marker != "" && candidate.key <= page.marker && !strings.HasPrefix(page.marker, candidate.key) {
			continue
		}
		keepGoing, err := b.walkPage(ctx, bucket, strings.TrimSuffix(candidate.key, "/"), "", false, page, markers, false)
		if err != nil && err != gofakes3.ErrNoSuchKey {
			return keepGoing, err
		}
		if !keepGoing {
			return false, nil
		}
	}
	return true, nil
}
