package cloudreve_v4

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/directupload"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const (
	// DirectUploadToolHttp lets the browser upload the payload straight to the
	// storage backend with plain HTTP.
	DirectUploadToolHttp = "HttpDirect"
	// directUploadDefaultTTL is the fallback lifetime of a pending session when
	// Cloudreve does not report one.
	directUploadDefaultTTL = 12 * time.Hour
)

// GetDirectUploadTools implements driver.DirectUploader. Only S3-like policies
// expose presigned part URLs, which is decided per directory in
// GetDirectUploadInfo because the tool list is reported per storage.
func (d *CloudreveV4) GetDirectUploadTools() []string {
	if d.DisableDirectUpload {
		return nil
	}
	return []string{DirectUploadToolHttp}
}

// GetDirectUploadInfo implements driver.DirectUploader. It opens an upload
// session on Cloudreve and hands the presigned part URLs to the client, so the
// payload never passes through this server. The session is finalized later by
// /fs/complete_direct_upload through the token embedded in CompleteURL.
func (d *CloudreveV4) GetDirectUploadInfo(ctx context.Context, tool string, dstDir model.Obj, fileName string, fileSize int64) (any, error) {
	if d.DisableDirectUpload || tool != DirectUploadToolHttp || d.isShare() || fileSize <= 0 {
		return nil, errs.NotImplement
	}
	// the completion endpoint is authenticated, so the session has to belong to
	// a real user
	user, ok := ctx.Value(conf.UserKey).(*model.User)
	if !ok {
		return nil, errs.NotImplement
	}
	contentType, _ := ctx.Value(conf.DirectUploadContentTypeKey).(string)
	u, err := d.createUploadSession(ctx, dstDir, fileName, fileSize, time.Now(), contentType, false)
	if err != nil {
		if !errs.IsObjectAlreadyExists(err) {
			return nil, err
		}
		// Cloudreve refuses to open a session for an existing file, so fall
		// back to storing the upload as a new version and keep the old content
		// instead of deleting it.
		u, err = d.createUploadSession(ctx, dstDir, fileName, fileSize, time.Now(), contentType, true)
		if err != nil {
			return nil, fmt.Errorf("failed to overwrite the existing file: %w", err)
		}
	}
	s3Type, ok := crS3Type(u)
	if !ok {
		d.deleteUploadSession(u)
		return nil, errs.NotImplement
	}
	totalChunks := (fileSize + u.ChunkSize - 1) / u.ChunkSize
	if int64(len(u.UploadUrls)) < totalChunks {
		d.deleteUploadSession(u)
		return nil, fmt.Errorf("cloudreve returned %d part urls for %d parts", len(u.UploadUrls), totalChunks)
	}
	headers := map[string]string{}
	if s3Type == "ks3" {
		// mirrors the server-side upload path: ks3 requires an explicit
		// content type, while s3 presigned URLs are signed without one
		headers["Content-Type"] = "application/octet-stream"
	}
	token, err := directupload.Register(&directupload.Session{
		UserID:  user.ID,
		Path:    utils.GetFullPath(d.GetStorage().MountPath, dstDir.GetPath()+"/"+fileName),
		Expires: time.Now().Add(directUploadTTL(u)),
		Finish: func(ctx context.Context, etags []string) error {
			if int64(len(etags)) != totalChunks {
				return fmt.Errorf("expected %d part etags, got %d", totalChunks, len(etags))
			}
			return d.completeS3Upload(ctx, u, s3Type, etags)
		},
		Abort: func(context.Context) {
			d.deleteUploadSession(u)
		},
	})
	if err != nil {
		d.deleteUploadSession(u)
		return nil, err
	}
	return &model.HttpDirectUploadInfo{
		UploadURLs:     u.UploadUrls[:totalChunks],
		ChunkSize:      u.ChunkSize,
		Headers:        headers,
		Method:         http.MethodPut,
		CompleteURL:    "/fs/complete_direct_upload?token=" + token,
		CompleteMethod: http.MethodPost,
	}, nil
}

// crS3Type reports the Cloudreve S3-like policy type that can be uploaded with
// presigned part URLs.
func crS3Type(u FileUploadResp) (string, bool) {
	if u.StoragePolicy.Relay {
		// relay forces the payload through Cloudreve itself, so there is no
		// presigned URL to hand out
		return "", false
	}
	switch u.StoragePolicy.Type {
	case "s3", "ks3":
	default:
		return "", false
	}
	if u.ChunkSize <= 0 || len(u.UploadUrls) == 0 || u.CompleteURL == "" {
		return "", false
	}
	return u.StoragePolicy.Type, true
}

func directUploadTTL(u FileUploadResp) time.Duration {
	if u.Expires <= 0 {
		return directUploadDefaultTTL
	}
	// Cloudreve reports the session deadline as a Unix timestamp; anything that
	// is already gone or too far away falls back to the default
	if ttl := time.Until(time.Unix(u.Expires, 0)); ttl > 5*time.Minute && ttl < directUploadDefaultTTL {
		return ttl
	}
	return directUploadDefaultTTL
}

var _ driver.DirectUploader = (*CloudreveV4)(nil)
