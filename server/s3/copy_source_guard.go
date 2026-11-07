package s3

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// copySourceIdentityFunc reports what a copy source looks like right now. ok is
// false when the source cannot be resolved (missing bucket or key), in which
// case the copy itself is left to report the error.
type copySourceIdentityFunc func(ctx context.Context, bucket, key string) (etag string, mod time.Time, ok bool)

// copySourceConditionGuard enforces the x-amz-copy-source-if-* preconditions.
//
// gofakes3 copies the source unconditionally: a client that asked "copy only if
// the source is still the version I read" got a 200 and a copy of whatever was
// there, which is exactly the race the header exists to prevent. The check has
// to happen here because the preconditions live in the request headers and the
// storage interface never sees them.
func copySourceConditionGuard(next http.Handler, keys *keyRing, identity copySourceIdentityFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || identity == nil {
			next.ServeHTTP(w, r)
			return
		}
		ifMatch := strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source-If-Match"))
		ifNoneMatch := strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source-If-None-Match"))
		ifModifiedSince := strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source-If-Modified-Since"))
		ifUnmodifiedSince := strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source-If-Unmodified-Since"))
		if ifMatch == "" && ifNoneMatch == "" && ifModifiedSince == "" && ifUnmodifiedSince == "" {
			next.ServeHTTP(w, r)
			return
		}
		sourceBucket, sourceKey, present := copySourceRef(r)
		if !present || sourceBucket == "" || sourceKey == "" {
			// A malformed source is rejected further down; nothing to compare.
			next.ServeHTTP(w, r)
			return
		}
		// The source bucket is named in a header, so the guard has to ask whether
		// the caller may read it at all. bucketAuthHandler answers 403 for a
		// source the key is not scoped to, but it runs behind this guard: the
		// 412 answered here for a mismatched tag, and the pass-through for a
		// matching one, told an unauthorised caller whether that key exists.
		if !s3BucketReadAllowed(r, sourceBucket) {
			next.ServeHTTP(w, r)
			return
		}
		etag, mod, ok := identity(r.Context(), sourceBucket, sourceKey)
		if !ok {
			// Unknown source: let the copy report NoSuchBucket / NoSuchKey.
			next.ServeHTTP(w, r)
			return
		}
		if copySourcePreconditionFailed(ifMatch, ifNoneMatch, ifModifiedSince, ifUnmodifiedSince, etag, mod) {
			writeS3Error(w, r, http.StatusPreconditionFailed, "PreconditionFailed", sourceBucket,
				"At least one of the preconditions you specified did not hold.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// copySourcePreconditionFailed evaluates the preconditions in the order
// RFC 7232 gives them: an entity tag wins over the corresponding date.
func copySourcePreconditionFailed(ifMatch, ifNoneMatch, ifModifiedSince, ifUnmodifiedSince, etag string, mod time.Time) bool {
	// An ETag we cannot name (no metadata row yet) cannot satisfy a positive
	// match: the client is asking for a specific version and we cannot prove it
	// is the one on disk, so the copy is refused rather than taken on trust.
	if ifMatch != "" {
		if !etagMatches(ifMatch, etag) {
			return true
		}
	} else if ifUnmodifiedSince != "" {
		if deadline, err := http.ParseTime(ifUnmodifiedSince); err == nil {
			if mod.Truncate(time.Second).After(deadline) {
				return true
			}
		}
	}
	if ifNoneMatch != "" {
		if etagMatches(ifNoneMatch, etag) {
			return true
		}
	} else if ifModifiedSince != "" && ifMatch == "" {
		if since, err := http.ParseTime(ifModifiedSince); err == nil {
			if !mod.Truncate(time.Second).After(since) {
				return true
			}
		}
	}
	return false
}

// etagMatches compares an If-[None-]Match header value against the current
// entity tag. Values arrive quoted, comma separated, and possibly as "*".
func etagMatches(header, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "*" {
		// A wildcard matches any current representation. With no ETag to name
		// the object still exists, so this holds.
		return true
	}
	if etag == "" {
		return false
	}
	current := strings.Trim(etag, `"`)
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" {
			return true
		}
		if strings.HasPrefix(candidate, "W/") {
			// A weak tag can never be a strong match.
			continue
		}
		if strings.Trim(candidate, `"`) == current {
			return true
		}
	}
	return false
}

// zeroCopySourceIdentity resolves the current ETag and modification time of a
// copy source the way HEAD does, so a precondition compares against the value
// the client was actually given.
func (b *s3Backend) zeroCopySourceIdentity(ctx context.Context, bucketName, key string) (string, time.Time, bool) {
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return "", time.Time{}, false
	}
	fp, err := objectFSPath(bucket.Path, key)
	if err != nil {
		return "", time.Time{}, false
	}
	fmeta, _ := op.GetNearestMeta(fp)
	node, err := fs.Get(context.WithValue(ctx, conf.MetaKey, fmeta), fp, &fs.GetArgs{})
	if err != nil || node.IsDir() {
		return "", time.Time{}, false
	}
	hash, err := b.objectHashForBucket(ctx, bucketName, fp, node)
	if err != nil {
		return "", time.Time{}, false
	}
	_, etag := b.objectMetadataOf(ctx, fp, hash)
	if etag == "" && len(hash) > 0 {
		etag = fmt.Sprintf("%q", hex.EncodeToString(hash))
	}
	return etag, node.ModTime(), true
}
