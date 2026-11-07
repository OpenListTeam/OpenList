// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"context"
	"net/http"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/gofakes3/signature"
)

func redirectHandler(next http.Handler, keys *keyRing, tracker *s3Backend) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := directObjectURL(r, keys, tracker); ok {
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
			w.Header().Set("Location", u)
			w.WriteHeader(http.StatusFound)
			return
		}
		if u, ok := directUploadURL(r, keys); ok {
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Location", u)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func directObjectURL(r *http.Request, keys *keyRing, tracker *s3Backend) (string, bool) {
	if r.Method != http.MethodGet {
		return "", false
	}
	if hasNonObjectQuery(r) || hasResponseOverride(r) || !s3RequestAuthorized(r, keys) {
		return "", false
	}
	bucketName, objectName, ok := parseObjectPath(r.URL.Path)
	if !ok || !s3BucketReadAllowed(r, bucketName) {
		return "", false
	}
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return "", false
	}
	reqPath, err := objectFSPath(bucket.Path, objectName)
	if err != nil {
		return "", false
	}
	meta, _ := op.GetNearestMeta(reqPath)
	ctx := context.WithValue(r.Context(), conf.MetaKey, meta)
	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	if err != nil || common.ShouldProxy(storage, path.Base(reqPath)) {
		return "", false
	}
	link, file, err := fs.Link(ctx, reqPath, model.LinkArgs{
		IP:       utils.ClientIP(r),
		Header:   r.Header,
		Redirect: true,
	})
	if err != nil {
		return "", false
	}
	defer link.Close()
	if file == nil || file.IsDir() || link.URL == "" || link.RangeReader != nil {
		return "", false
	}
	// The bucket is named so the access row records which tier served the file;
	// this path leaves through a 302, so the backend never sees the request.
	recordFileRead(bucketOnlyName(r.URL.Path), reqPath, file)
	// The 302 leaves through the storage provider, so the S3 access table has
	// to be maintained here: the backend never sees this request. Without it
	// /api/admin/s3/access - the only read history the cold/hot tiering looks
	// at - reports "never read" for every object served by a direct link, i.e.
	// for every storage that supports one.
	if tracker != nil {
		tracker.recordAccess(reqPath)
	}
	return link.URL, true
}

func directUploadURL(r *http.Request, keys *keyRing) (string, bool) {
	if r.Method != http.MethodPut || r.ContentLength < 0 {
		return "", false
	}
	if hasNonObjectQuery(r) || !s3RequestAuthorized(r, keys) {
		return "", false
	}
	// A body that still has to be de-chunked must not be forwarded: the
	// redirect hands the bytes to storage verbatim, while the proxied path
	// decodes aws-chunked framing. Storing the framed bytes corrupted every
	// upload from an SDK that signs a stream (STREAMING-UNSIGNED-PAYLOAD-TRAILER,
	// STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER, ...) or sends aws-chunked.
	if r.Header.Get("X-Amz-Copy-Source") != "" ||
		strings.HasPrefix(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING-") ||
		r.Header.Get("X-Amz-Decoded-Content-Length") != "" ||
		strings.Contains(strings.ToLower(r.Header.Get("Content-Encoding")), "aws-chunked") {
		return "", false
	}
	bucketName, objectName, ok := parseObjectPath(r.URL.Path)
	if !ok || strings.HasSuffix(objectName, "/") || !s3BucketAuthorized(r, bucketName) || !s3BucketCanWrite(r) {
		return "", false
	}
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return "", false
	}
	reqPath, err := objectFSPath(bucket.Path, objectName)
	if err != nil {
		return "", false
	}
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(path.Dir(reqPath))
	if err != nil || storage.Config().NoUpload {
		return "", false
	}
	info, err := op.GetDirectUploadInfo(r.Context(), "HttpDirect", storage, dstDirActualPath, path.Base(reqPath), r.ContentLength, true)
	if err != nil {
		return "", false
	}
	httpInfo, ok := asHTTPDirectUploadInfo(info)
	if !ok {
		return "", false
	}
	method := httpInfo.Method
	if method == "" {
		method = http.MethodPut
	}
	if !strings.EqualFold(method, http.MethodPut) || httpInfo.UploadURL == "" ||
		httpInfo.ChunkSize > 0 || len(httpInfo.Headers) > 0 {
		return "", false
	}
	return httpInfo.UploadURL, true
}

func asHTTPDirectUploadInfo(info any) (*model.HttpDirectUploadInfo, bool) {
	switch v := info.(type) {
	case *model.HttpDirectUploadInfo:
		return v, v != nil
	case model.HttpDirectUploadInfo:
		return &v, true
	default:
		return nil, false
	}
}

func parseObjectPath(rawPath string) (bucket, object string, ok bool) {
	parts := strings.SplitN(strings.TrimPrefix(rawPath, "/"), "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// hasResponseOverride reports whether the request asks the gateway to rewrite
// a response header (response-content-disposition=attachment;filename=...,
// response-content-type, response-cache-control, response-expires, ...).
//
// Those parameters are implemented by writing the response, so a direct link
// cannot honour them: the provider would have to support each one itself. Such
// a request therefore stays on the proxy path, where the override applies, and
// keeps working the same way whatever storage the object lives on.
func hasResponseOverride(r *http.Request) bool {
	for name := range r.URL.Query() {
		if strings.HasPrefix(strings.ToLower(name), "response-") {
			return true
		}
	}
	return false
}

// hasNonObjectQuery reports whether the request carries a query parameter that
// makes gofakes3 route it to a sub-resource handler instead of plain object
// download (see gofakes3 routing.go). Only those keys force server-side
// handling; every other query parameter (response-content-disposition,
// response-content-type, etc.) is irrelevant to route selection and must not
// block a direct redirect.
func hasNonObjectQuery(r *http.Request) bool {
	query := r.URL.Query()
	for _, key := range []string{"uploadId", "uploads", "versioning", "versions", "location", "cors", "delete"} {
		if _, ok := query[key]; ok {
			return true
		}
	}
	// Every subresource unsupportedSubresources answers must also be refused
	// here: the redirect runs earlier in the chain, so `PUT /bucket/key?tagging`
	// used to be forwarded as an object upload and the <Tagging> document was
	// stored as the object's body.
	for key := range query {
		if unsupportedSubresourceNames[key] {
			return true
		}
	}
	if versionID := query.Get("versionId"); versionID != "" && versionID != "null" {
		return true
	}
	return false
}

func s3RequestAuthorized(r *http.Request, keys *keyRing) bool {
	if keys == nil || keys.empty() {
		// No key configured at all: upstream treats such a gateway as open, so
		// the direct-transfer redirect stays available. A setting that is
		// present but unusable also empties the ring, but that state has to be
		// refused: bucketAuthHandler would answer 503 for the very same request
		// a layer deeper, so redirecting it straight to storage would skip the
		// refusal.
		return !authMisconfigured()
	}
	// Verify against the keys this server was configured with. V4SignVerify and
	// V2SignVerify read the signature package's process-wide key store, which
	// gofakes3 never writes (keys are kept per instance), so they always
	// returned InvalidAccessKeyId and the 302/307 direct-transfer redirects
	// never ran. Same V4-then-V2 order the auth middleware uses.
	result := signature.V4SignVerifyWithLookup(r, keys.secret)
	if result == signature.ErrUnsupportAlgorithm {
		result = signature.V2SignVerifyWithLookup(r, keys.secret)
	}
	return result == signature.ErrNone
}
