package guangyapan

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
	"golang.org/x/time/rate"
)

// --- HTTP request helpers ---

func (d *GuangYaPan) accountErr(desc, short string, resp *resty.Response) string {
	msg := strings.TrimSpace(desc)
	if msg == "" {
		msg = strings.TrimSpace(short)
	}
	if msg == "" && resp != nil {
		msg = strings.TrimSpace(resp.String())
	}
	if msg == "" && resp != nil {
		msg = fmt.Sprintf("status=%d", resp.StatusCode())
	}
	if msg == "" {
		msg = "unknown error"
	}
	return msg
}

func (d *GuangYaPan) apiRateLimitWait(ctx context.Context, path string) error {
	value, _ := d.apiRateLimit.LoadOrStore(path, rate.NewLimiter(rate.Every(apiRateInterval), 1))
	return value.(*rate.Limiter).Wait(ctx)
}

func (d *GuangYaPan) postAPI(ctx context.Context, path string, body any, out any) error {
	if err := d.ensureAccessToken(ctx); err != nil {
		return err
	}
	if err := d.apiRateLimitWait(ctx, path); err != nil {
		return err
	}
	resp, err := d.apiClient.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetBody(body).
		SetResult(out).
		Post(path)
	if err != nil {
		return err
	}
	if resp.StatusCode() == 401 || resp.StatusCode() == 403 {
		if strings.TrimSpace(d.RefreshToken) == "" {
			return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode(), resp.String())
		}
		if err := d.refreshToken(ctx); err != nil {
			return err
		}
		resp, err = d.apiClient.R().
			SetContext(ctx).
			SetHeader("Authorization", "Bearer "+d.AccessToken).
			SetBody(body).
			SetResult(out).
			Post(path)
		if err != nil {
			return err
		}
	}
	if resp.IsError() {
		return fmt.Errorf("request failed: status=%d body=%s", resp.StatusCode(), resp.String())
	}
	return nil
}

func (d *GuangYaPan) setTempStatus(status string) {
	if d.statusTimer != nil {
		d.statusTimer.Stop()
	}
	// initStorage sets status to WORK after Init returns, so we update it shortly after.
	d.statusTimer = time.AfterFunc(200*time.Millisecond, func() {
		d.GetStorage().SetStatus(status)
		op.MustSaveDriverStorage(d)
	})
}

// --- Task polling helpers ---

func (d *GuangYaPan) waitTaskDone(ctx context.Context, taskID string) error {
	const (
		maxTry   = 30
		interval = 300 * time.Millisecond
	)
	for i := 0; i < maxTry; i++ {
		var out taskStatusResp
		if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_task_status", map[string]any{
			"taskId": taskID,
		}, &out); err != nil {
			return err
		}
		if !isSuccessMsg(out.Msg) {
			return fmt.Errorf("get task status failed: %s", strings.TrimSpace(out.Msg))
		}
		switch out.Data.Status {
		case 2:
			return nil
		case -1, 3:
			return fmt.Errorf("task %s failed with status=%d", taskID, out.Data.Status)
		}
		if i == maxTry-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("task %s timeout", taskID)
}

// --- Upload helpers ---

func (d *GuangYaPan) getUploadToken(ctx context.Context, parentID, name string, size int64) (*uploadTokenData, int, error) {
	var out uploadTokenResp
	err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_res_center_token", map[string]any{
		"capacity": 2,
		"name":     name,
		"parentId": parentID,
		"res": map[string]any{
			"fileSize": size,
		},
	}, &out)
	if err != nil {
		return nil, 0, err
	}
	msg := strings.TrimSpace(out.Msg)
	if !isSuccessMsg(msg) && !isUploadAlreadyDone(msg) {
		return nil, out.Code, fmt.Errorf("get upload token failed: %s", msg)
	}
	if out.Data.TaskID == "" {
		return nil, out.Code, errors.New("get upload token failed: empty task id")
	}
	// When the backend reports the file is already uploaded/instant-uploaded,
	// it returns a valid TaskID without OSS credentials.
	// Mark it so the caller can skip the real upload and just wait for the task.
	if out.Code == 156 || isUploadAlreadyDone(msg) {
		out.Data.AlreadyDone = true
	}
	if out.Data.AccessKeyID == "" {
		out.Data.AccessKeyID = out.Data.Creds.AccessKeyID
	}
	if out.Data.SecretAccessKey == "" {
		out.Data.SecretAccessKey = out.Data.Creds.SecretAccessKey
	}
	if out.Data.SessionToken == "" {
		out.Data.SessionToken = out.Data.Creds.SessionToken
	}
	if strings.TrimSpace(out.Data.EndPoint) == "" {
		out.Data.EndPoint = strings.TrimSpace(out.Data.FullEndPoint)
	}
	if strings.TrimSpace(out.Data.EndPoint) != "" && !strings.HasPrefix(out.Data.EndPoint, "http://") && !strings.HasPrefix(out.Data.EndPoint, "https://") {
		if strings.TrimSpace(out.Data.FullEndPoint) != "" {
			out.Data.EndPoint = strings.TrimSpace(out.Data.FullEndPoint)
		} else if strings.TrimSpace(out.Data.BucketName) != "" {
			host := strings.TrimSpace(out.Data.EndPoint)
			prefix := strings.TrimSpace(out.Data.BucketName) + "."
			if strings.HasPrefix(host, prefix) {
				out.Data.EndPoint = "https://" + host
			} else {
				out.Data.EndPoint = "https://" + strings.TrimSpace(out.Data.BucketName) + "." + host
			}
		} else {
			out.Data.EndPoint = "https://" + strings.TrimSpace(out.Data.EndPoint)
		}
	}
	return &out.Data, out.Code, nil
}

func (d *GuangYaPan) waitUploadTaskInfo(ctx context.Context, taskID string) error {
	const (
		maxTry   = 300
		interval = 1 * time.Second
	)
	for i := 0; i < maxTry; i++ {
		var out taskInfoResp
		if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/get_info_by_task_id", map[string]any{
			"taskId": taskID,
		}, &out); err != nil {
			return err
		}
		if out.Data.FileID != "" {
			return nil
		}
		switch out.Code {
		case 145, 146, 147, 155, 163, 0:
			// uploading/verifying/processing
		default:
			if strings.TrimSpace(out.Msg) != "" {
				return fmt.Errorf("upload task failed: code=%d msg=%s", out.Code, strings.TrimSpace(out.Msg))
			}
		}
		if i == maxTry-1 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
	return fmt.Errorf("upload task %s timeout", taskID)
}

func (d *GuangYaPan) multipartUploadToOSS(ctx context.Context, bucket *oss.Bucket, objectPath string, file model.FileStreamer, up driver.UpdateProgress) error {
	partSize := calcUploadPartSize(file.GetSize())
	imur, err := bucket.InitiateMultipartUpload(objectPath, oss.Sequential())
	if err != nil {
		return err
	}

	total := file.GetSize()
	partCount := int((total + partSize - 1) / partSize)

	// Use StreamSectionReader for seekable, retryable chunk reads (hybrid cache).
	ss, err := streamPkg.NewStreamSectionReader(file, int(partSize), &up)
	if err != nil {
		return err
	}

	parts := make([]oss.UploadPart, 0, partCount)
	for i := 0; i < partCount; i++ {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}

		offset := int64(i) * partSize
		length := partSize
		if remain := total - offset; length > remain {
			length = remain
		}

		rd, err := ss.GetSectionReader(offset, length)
		if err != nil {
			return err
		}

		var part oss.UploadPart
		err = retry.Do(func() error {
			rd.Seek(0, io.SeekStart)
			var uploadErr error
			part, uploadErr = bucket.UploadPart(imur, driver.NewLimitedUploadStream(ctx, rd), length, i+1)
			return uploadErr
		},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second))
		ss.FreeSectionReader(rd)
		if err != nil {
			return fmt.Errorf("failed to upload part %d: %w", i+1, err)
		}
		parts = append(parts, part)
	}

	_, err = bucket.CompleteMultipartUpload(imur, parts)
	return err
}

// --- Normalization helpers ---

func normalizeOSSEndpoint(endpoint, bucket string) string {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		return ep
	}
	if !strings.HasPrefix(ep, "http://") && !strings.HasPrefix(ep, "https://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return ep
	}
	host := u.Host
	prefix := strings.TrimSpace(bucket)
	if prefix != "" && strings.HasPrefix(host, prefix+".") {
		host = strings.TrimPrefix(host, prefix+".")
	}
	u.Host = host
	return u.String()
}

func normalizeDeviceID(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "")
	if len(v) != 32 {
		return ""
	}
	for _, ch := range v {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	return v
}

func randomDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "0123456789abcdef0123456789abcdef"
	}
	return hex.EncodeToString(b)
}

func normalizeCaptchaUsername(phone string) string {
	p := strings.TrimSpace(phone)
	p = strings.ReplaceAll(p, " ", "")
	p = strings.TrimPrefix(p, "+")
	// Keep only digits.
	b := make([]rune, 0, len(p))
	for _, ch := range p {
		if ch >= '0' && ch <= '9' {
			b = append(b, ch)
		}
	}
	digits := string(b)
	// Mainland number normalization: +86xxxxxxxxxxx -> xxxxxxxxxxx
	if strings.HasPrefix(digits, "86") && len(digits) > 11 {
		digits = digits[2:]
	}
	return digits
}

func normalizePhoneE164(phone string) string {
	p := strings.TrimSpace(phone)
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, " ", "")
	if strings.HasPrefix(p, "+") {
		// Format as "+86 1xxxxxxxxxx" to match browser payload expectations.
		if strings.HasPrefix(p, "+86") && len(p) > 3 {
			rest := strings.TrimPrefix(p, "+86")
			return "+86 " + rest
		}
		return p
	}
	// If raw mainland number is provided, normalize with +86 prefix.
	digits := normalizeCaptchaUsername(p)
	if len(digits) == 11 {
		return "+86 " + digits
	}
	return p
}

func calcUploadPartSize(size int64) int64 {
	const (
		mb = int64(1024 * 1024)
		gb = int64(1024 * 1024 * 1024)
	)
	switch {
	case size <= 100*mb:
		return 1 * mb
	case size <= 16*gb:
		return 2 * mb
	case size <= 160*gb:
		return 4 * mb
	default:
		return 8 * mb
	}
}

// isUploadAlreadyDone reports whether the upload-token response indicates the
// file was already uploaded (instant upload). In that case the backend returns
// a valid TaskID but no OSS credentials, and we should just wait for the task
// instead of starting a real upload.
func isUploadAlreadyDone(msg string) bool {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return false
	}
	if strings.EqualFold(msg, "上传已完成") {
		return true
	}
	if strings.EqualFold(msg, "upload completed") {
		return true
	}
	if strings.EqualFold(msg, "already uploaded") {
		return true
	}
	if strings.EqualFold(msg, "秒传成功") {
		return true
	}
	return false
}
