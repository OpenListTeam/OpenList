package pds

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

const (
	directUploadTool     = "PdsDirect"
	directUploadTokenTTL = 2 * time.Hour
)

type directUploadToken struct {
	DomainID     string `json:"domain_id"`
	DriveID      string `json:"drive_id"`
	ParentFileID string `json:"parent_file_id"`
	FileID       string `json:"file_id"`
	UploadID     string `json:"upload_id"`
	FileName     string `json:"file_name"`
	FileSize     int64  `json:"file_size"`
	ExpiresAt    int64  `json:"expires_at"`
}

type directUploadInfo struct {
	UploadURL string                      `json:"upload_url"`
	Headers   map[string]string           `json:"headers,omitempty"`
	Method    string                      `json:"method,omitempty"`
	Complete  *directUploadCompletionInfo `json:"complete,omitempty"`
}

type directUploadCompletionInfo struct {
	URL     string            `json:"url,omitempty"`
	Method  string            `json:"method,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    map[string]any    `json:"body,omitempty"`
}

func (d *PDS) GetDirectUploadTools() []string {
	return []string{directUploadTool}
}

func (d *PDS) GetDirectUploadInfo(ctx context.Context, tool string, dstDir model.Obj, fileName string, fileSize int64) (any, error) {
	if tool != directUploadTool {
		return nil, errs.NotImplement
	}
	if fileSize < 0 {
		return nil, fmt.Errorf("file_size is required for PDS direct upload")
	}
	var created createFileResp
	err := d.client.post(ctx, "/v2/file/create", map[string]any{
		"drive_id":        d.DriveID,
		"parent_file_id":  d.fileID(dstDir),
		"name":            fileName,
		"type":            "file",
		"check_name_mode": "auto_rename",
		"size":            fileSize,
		"part_info_list":  []map[string]int{{"part_number": 1}},
	}, &created)
	if err != nil {
		return nil, err
	}
	if len(created.PartInfoList) == 0 || created.PartInfoList[0].UploadURL == "" {
		return nil, fmt.Errorf("pds create file did not return upload_url")
	}

	uploadToken, err := d.signDirectUploadToken(directUploadToken{
		DomainID:     d.DomainID,
		DriveID:      d.DriveID,
		ParentFileID: d.fileID(dstDir),
		FileID:       created.FileID,
		UploadID:     created.UploadID,
		FileName:     fileName,
		FileSize:     fileSize,
		ExpiresAt:    time.Now().Add(directUploadTokenTTL).Unix(),
	})
	if err != nil {
		return nil, err
	}

	apiURL := common.GetApiUrl(ctx)
	if apiURL == "" {
		apiURL = "/"
	}
	completeURL := strings.TrimRight(apiURL, "/") + "/api/fs/complete_direct_upload"
	return &directUploadInfo{
		UploadURL: created.PartInfoList[0].UploadURL,
		Method:    http.MethodPut,
		Headers: map[string]string{
			"Content-Type": "",
		},
		Complete: &directUploadCompletionInfo{
			URL:    completeURL,
			Method: http.MethodPost,
			Headers: map[string]string{
				"Content-Type": "application/json",
			},
			Body: map[string]any{
				"path":         utils.GetFullPath(d.GetStorage().MountPath, dstDir.GetPath()),
				"file_name":    fileName,
				"tool":         directUploadTool,
				"upload_token": uploadToken,
			},
		},
	}, nil
}

func (d *PDS) CompleteDirectUpload(ctx context.Context, tool string, dstDir model.Obj, fileName string, uploadToken string) (model.Obj, error) {
	if tool != directUploadTool {
		return nil, errs.NotImplement
	}
	token, err := d.verifyDirectUploadToken(uploadToken)
	if err != nil {
		return nil, err
	}
	if token.DomainID != d.DomainID || token.DriveID != d.DriveID ||
		token.ParentFileID != d.fileID(dstDir) {
		return nil, fmt.Errorf("direct upload token does not match request")
	}
	if token.FileID == "" || token.UploadID == "" {
		return nil, fmt.Errorf("direct upload token is incomplete")
	}
	var completed createFileResp
	err = d.client.post(ctx, "/v2/file/complete", map[string]any{
		"drive_id":  token.DriveID,
		"file_id":   token.FileID,
		"upload_id": token.UploadID,
	}, &completed)
	if err != nil {
		return nil, err
	}
	fileID := completed.FileID
	if fileID == "" {
		fileID = token.FileID
	}
	obj, err := d.getFileObj(ctx, fileID)
	if err != nil {
		return nil, err
	}
	return withParentPath(dstDir.GetPath(), obj), nil
}

func (d *PDS) signDirectUploadToken(token directUploadToken) (string, error) {
	payload, err := json.Marshal(token)
	if err != nil {
		return "", err
	}
	payloadText := base64.RawURLEncoding.EncodeToString(payload)
	signature, err := d.signDirectUploadPayload(payloadText)
	if err != nil {
		return "", err
	}
	return payloadText + "." + signature, nil
}

func (d *PDS) verifyDirectUploadToken(raw string) (*directUploadToken, error) {
	payloadText, signature, ok := strings.Cut(raw, ".")
	if !ok || payloadText == "" || signature == "" {
		return nil, fmt.Errorf("invalid direct upload token")
	}
	expected, err := d.signDirectUploadPayload(payloadText)
	if err != nil {
		return nil, err
	}
	if !hmac.Equal([]byte(signature), []byte(expected)) {
		return nil, fmt.Errorf("invalid direct upload token signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadText)
	if err != nil {
		return nil, fmt.Errorf("invalid direct upload token payload: %w", err)
	}
	var token directUploadToken
	if err := json.Unmarshal(payload, &token); err != nil {
		return nil, err
	}
	if token.ExpiresAt > 0 && time.Now().Unix() > token.ExpiresAt {
		return nil, fmt.Errorf("direct upload token expired")
	}
	return &token, nil
}

func (d *PDS) signDirectUploadPayload(payload string) (string, error) {
	secret := d.directUploadSecret()
	if len(secret) == 0 {
		return "", fmt.Errorf("direct upload token secret is empty")
	}
	mac := hmac.New(sha256.New, secret)
	if _, err := mac.Write([]byte(payload)); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (d *PDS) directUploadSecret() []byte {
	if conf.Conf != nil && conf.Conf.JwtSecret != "" {
		return []byte(conf.Conf.JwtSecret)
	}
	if d.RefreshToken != "" {
		return []byte(d.RefreshToken)
	}
	return []byte(d.AccessToken)
}

var _ driver.DirectUploader = (*PDS)(nil)
var _ driver.DirectUploadCompleter = (*PDS)(nil)
