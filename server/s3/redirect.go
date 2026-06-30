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
	"github.com/itsHenry35/gofakes3/signature"
)

func redirectHandler(next http.Handler, authPairs map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := directObjectURL(r, authPairs); ok {
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Cache-Control", "max-age=0, no-cache, no-store, must-revalidate")
			w.Header().Set("Location", u)
			w.WriteHeader(http.StatusFound)
			return
		}
		if u, ok := directUploadURL(r, authPairs); ok {
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("Location", u)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func directObjectURL(r *http.Request, authPairs map[string]string) (string, bool) {
	if r.Method != http.MethodGet {
		return "", false
	}
	if hasNonObjectQuery(r) || !s3RequestAuthorized(r, authPairs) {
		return "", false
	}
	bucketName, objectName, ok := parseObjectPath(r.URL.Path)
	if !ok {
		return "", false
	}
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return "", false
	}
	reqPath := path.Join(bucket.Path, objectName)
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
	return link.URL, true
}

func directUploadURL(r *http.Request, authPairs map[string]string) (string, bool) {
	if r.Method != http.MethodPut || r.ContentLength < 0 {
		return "", false
	}
	if hasNonObjectQuery(r) || !s3RequestAuthorized(r, authPairs) {
		return "", false
	}
	if r.Header.Get("X-Amz-Copy-Source") != "" ||
		r.Header.Get("X-Amz-Content-Sha256") == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" {
		return "", false
	}
	bucketName, objectName, ok := parseObjectPath(r.URL.Path)
	if !ok || strings.HasSuffix(objectName, "/") {
		return "", false
	}
	bucket, err := getBucketByName(bucketName)
	if err != nil {
		return "", false
	}
	reqPath := path.Join(bucket.Path, objectName)
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

// hasNonObjectQuery reports whether the request carries a query parameter that
// makes gofakes3 route it to a sub-resource handler instead of plain object
// download (see gofakes3 routing.go). Only those keys force server-side
// handling; every other query parameter (response-content-disposition,
// response-content-type, etc.) is irrelevant to route selection and must not
// block a direct redirect.
func hasNonObjectQuery(r *http.Request) bool {
	query := r.URL.Query()
	for _, key := range []string{"uploadId", "uploads", "versioning", "versions", "location"} {
		if _, ok := query[key]; ok {
			return true
		}
	}
	if versionID := query.Get("versionId"); versionID != "" && versionID != "null" {
		return true
	}
	return false
}

func s3RequestAuthorized(r *http.Request, authPairs map[string]string) bool {
	if len(authPairs) == 0 {
		return true
	}
	result := signature.V4SignVerify(r)
	if result == signature.ErrUnsupportAlgorithm {
		result = signature.V2SignVerify(r)
	}
	return result == signature.ErrNone
}
