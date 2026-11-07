package s3

import (
	"context"
	"net/http"
	"strings"
)

// deleteIdentityFunc reports what an object looks like right now: its ETag, and
// whether it exists at all.
type deleteIdentityFunc func(ctx context.Context, bucket, key string) (etag string, ok bool)

// deleteConditionGuard enforces `If-Match` on DELETE.
//
// A conditional delete is how a client says "remove this object only if it is
// still the one I looked at". Without the check the request deletes whatever is
// there, so a writer that raced us loses its object: the client asked for a
// guarantee it did not get.
func deleteConditionGuard(next http.Handler, keys *keyRing, identity deleteIdentityFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			next.ServeHTTP(w, r)
			return
		}
		ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
		if ifMatch == "" {
			next.ServeHTTP(w, r)
			return
		}
		bucket, key, ok := parseObjectPath(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		// An unauthenticated request must still be answered by the auth layer,
		// not by this guard: it would otherwise report on an object's existence.
		if !s3RequestAuthorized(r, keys) {
			next.ServeHTTP(w, r)
			return
		}
		// A valid signature is not a right to this bucket: a key scoped to
		// another bucket signs just as well. bucketAuthHandler, which answers 403
		// for such a request, runs behind this guard, so the 404/412 answered
		// here replaced that refusal with an existence oracle for every bucket of
		// the gateway.
		if !s3BucketAuthorized(r, bucket) || !s3BucketCanWriteTo(r, bucket) {
			next.ServeHTTP(w, r)
			return
		}
		etag, exists := identity(r.Context(), bucket, key)
		if !exists {
			writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", bucket,
				"The specified key does not exist.")
			return
		}
		if !etagMatches(ifMatch, etag) {
			writeS3Error(w, r, http.StatusPreconditionFailed, "PreconditionFailed", bucket,
				"At least one of the preconditions you specified did not hold.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// zeroDeleteIdentity resolves the ETag of an object from its hash and metadata.
func (b *s3Backend) zeroDeleteIdentity(ctx context.Context, bucketName, key string) (string, bool) {
	etag, _, ok := b.zeroCopySourceIdentity(ctx, bucketName, key)
	if !ok {
		return "", false
	}
	return etag, true
}
