package guangyapan

import (
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const (
	accountBaseURL = "https://account.guangyapan.com"
	apiBaseURL     = "https://api.guangyapan.com"
	defaultClient  = "aMe-8VSlkrbQXpUR"
)

type GuangYaPan struct {
	model.Storage
	Addition
	accountClient *resty.Client
	apiClient     *resty.Client
}

func (d *GuangYaPan) Config() driver.Config {
	return config
}

func (d *GuangYaPan) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *GuangYaPan) Init(ctx context.Context) error {
	d.accountClient = base.NewRestyClient()
	d.accountClient.SetBaseURL(accountBaseURL)
	d.accountClient.SetHeader("User-Agent", "Mozilla/5.0")
	d.accountClient.SetHeader("Accept", "application/json")

	d.apiClient = base.NewRestyClient()
	d.apiClient.SetBaseURL(apiBaseURL)
	d.apiClient.SetHeader("User-Agent", "Mozilla/5.0")
	d.apiClient.SetHeader("Accept", "application/json")

	if d.AccessToken != "" {
		d.apiClient.SetHeader("Authorization", "Bearer "+d.AccessToken)
	}

	if d.ClientID == "" {
		d.ClientID = defaultClient
	}
	if d.PageSize == 0 {
		d.PageSize = 100
	}

	return d.ensureAccessToken()
}

func (d *GuangYaPan) Drop(ctx context.Context) error {
	d.accountClient = nil
	d.apiClient = nil
	return nil
}

func (d *GuangYaPan) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var result []model.Obj
	page := 1
	pageSize := d.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}

	for {
		var resp listResp
		_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/get_file_list", map[string]interface{}{
			"parentId": dir.GetID(),
			"page":     page,
			"pageSize": pageSize,
			"orderBy":  d.OrderBy,
			"sord":     d.SortType,
		}, &resp)
		if err != nil {
			return nil, err
		}
		if resp.Code != 0 {
			return nil, fmt.Errorf("list failed: %s", resp.Msg)
		}

		for _, item := range resp.Data.List {
			file := File{fileItem: item}
			result = append(result, file)
		}

		if len(result) >= resp.Data.Total || len(resp.Data.List) < pageSize {
			break
		}
		page++
	}

	return result, nil
}

func (d *GuangYaPan) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	f, ok := file.(File)
	if !ok {
		return nil, fmt.Errorf("invalid file type")
	}

	var resp downloadResp
	_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_res_download_url", map[string]interface{}{
		"fileId": f.FileID,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("get download link failed: %s", resp.Msg)
	}

	link := &model.Link{
		URL: resp.Data.DownloadURL,
	}
	if resp.Data.SignedURL != "" {
		link.URL = resp.Data.SignedURL
	}

	return link, nil
}

func (d *GuangYaPan) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	var resp createDirResp
	_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/create_dir", map[string]interface{}{
		"parentId": parentDir.GetID(),
		"fileName": dirName,
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("mkdir failed: %s", resp.Msg)
	}
	return nil
}

func (d *GuangYaPan) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	var resp commonResp
	_, err := d.postAPI(ctx, "/v1/file/rename", map[string]interface{}{
		"fileId":   srcObj.GetID(),
		"fileName": newName,
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("rename failed: %s", resp.Msg)
	}
	return nil
}

func (d *GuangYaPan) Remove(ctx context.Context, obj model.Obj) error {
	var resp deleteResp
	_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/delete_file", map[string]interface{}{
		"fileIds": []string{obj.GetID()},
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("delete failed: %s", resp.Msg)
	}

	if resp.Data.TaskID != "" {
		return d.waitTaskDone(ctx, resp.Data.TaskID)
	}
	return nil
}

func (d *GuangYaPan) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	var resp commonResp
	_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/move_file", map[string]interface{}{
		"fileIds":   []string{srcObj.GetID()},
		"parentId":  dstDir.GetID(),
	}, &resp)
	if err != nil {
		return err
	}
	if resp.Code != 0 {
		return fmt.Errorf("move failed: %s", resp.Msg)
	}
	return nil
}

func (d *GuangYaPan) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *GuangYaPan) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	token, err := d.getUploadToken(ctx, dstDir.GetID(), file.GetName(), file.GetSize())
	if err != nil {
		return err
	}

	return d.multipartUploadToOSS(ctx, token, file, up)
}

// File type wrapper
type File struct {
	fileItem
}

func (f File) CreateTime() time.Time {
	return unixOrZero(f.CTime)
}

func (f File) GetHash() utils.HashInfo {
	return utils.HashInfo{}
}

func (f File) GetPath() string {
	return ""
}

func (f File) GetSize() int64 {
	return f.FileSize
}

func (f File) GetName() string {
	return f.FileName
}

func (f File) ModTime() time.Time {
	return unixOrZero(f.UTime)
}

func (f File) IsDir() bool {
	return f.ResType == 2
}

func (f File) GetID() string {
	return f.FileID
}

var _ model.Obj = (*File)(nil)

// Helper methods
func (d *GuangYaPan) ensureAccessToken() error {
	if d.AccessToken == "" && d.RefreshToken != "" {
		return d.refreshToken()
	}
	if d.AccessToken == "" && d.canSMSLogin() {
		return d.loginBySMSCode()
	}
	return nil
}

func (d *GuangYaPan) canSMSLogin() bool {
	return d.PhoneNumber != "" && d.VerifyCode != ""
}

func (d *GuangYaPan) loginBySMSCode() error {
	if d.VerificationID == "" {
		if err := d.requestVerificationID(); err != nil {
			return err
		}
	}

	var verifyResp verifyResp
	_, err := d.accountClient.R().SetBody(map[string]interface{}{
		"verification_id": d.VerificationID,
		"verification_code": d.VerifyCode,
	}).SetResult(&verifyResp).Post("/v1/auth/verification/verify")
	if err != nil {
		return err
	}
	if verifyResp.Error != "" {
		return fmt.Errorf("verify SMS code failed: %s", verifyResp.ErrorDesc)
	}

	var tokenResp tokenResp
	_, err = d.accountClient.R().SetBody(map[string]interface{}{
		"verification_token": verifyResp.VerificationToken,
		"client_id":          d.ClientID,
	}).SetResult(&tokenResp).Post("/v1/auth/token")
	if err != nil {
		return err
	}
	if tokenResp.Error != "" {
		return fmt.Errorf("get token failed: %s", tokenResp.ErrorDesc)
	}

	d.AccessToken = tokenResp.AccessToken
	d.RefreshToken = tokenResp.RefreshToken
	d.VerifyCode = ""
	d.VerificationID = ""

	d.apiClient.SetHeader("Authorization", "Bearer "+d.AccessToken)
	
	// Persist tokens
	op.MustSaveDriverStorage(d)

	return nil
}

func (d *GuangYaPan) requestVerificationID() error {
	if d.CaptchaToken == "" {
		if err := d.ensureCaptchaToken(); err != nil {
			return err
		}
	}

	phone := d.normalizePhoneE164(d.PhoneNumber)
	var resp verificationResp
	_, err := d.accountClient.R().SetBody(map[string]interface{}{
		"username":     phone,
		"captcha_token": d.CaptchaToken,
		"send_sms":     true,
	}).SetResult(&resp).Post("/v1/auth/verification")
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("request verification ID failed: %s", resp.ErrorDesc)
	}

	d.VerificationID = resp.VerificationID
	d.SendCode = false

	return nil
}

func (d *GuangYaPan) ensureCaptchaToken() error {
	var resp captchaInitResp
	_, err := d.accountClient.R().SetBody(map[string]interface{}{
		"client_id": d.ClientID,
	}).SetResult(&resp).Post("/v1/captcha/init")
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("get captcha token failed: %s", resp.ErrorDesc)
	}

	d.CaptchaToken = resp.CaptchaToken
	return nil
}

func (d *GuangYaPan) refreshToken() error {
	var resp tokenResp
	_, err := d.accountClient.R().SetBody(map[string]interface{}{
		"grant_type":    "refresh_token",
		"refresh_token": d.RefreshToken,
		"client_id":     d.ClientID,
	}).SetResult(&resp).Post("/v1/auth/token")
	if err != nil {
		return err
	}
	if resp.Error != "" {
		return fmt.Errorf("refresh token failed: %s", resp.ErrorDesc)
	}

	d.AccessToken = resp.AccessToken
	d.RefreshToken = resp.RefreshToken

	d.apiClient.SetHeader("Authorization", "Bearer "+d.AccessToken)
	
	// Persist tokens
	op.MustSaveDriverStorage(d)

	return nil
}

func (d *GuangYaPan) normalizePhoneE164(phone string) string {
	phone = strings.TrimSpace(phone)
	if !strings.HasPrefix(phone, "+") {
		phone = "+86" + phone
	}
	return phone
}

func (d *GuangYaPan) postAPI(ctx context.Context, apiPath string, body interface{}, result interface{}) (*resty.Response, error) {
	if err := d.ensureAccessToken(); err != nil {
		return nil, err
	}

	resp, err := d.apiClient.R().SetContext(ctx).SetBody(body).SetResult(result).Post(apiPath)
	if err != nil {
		return resp, err
	}

	if r, ok := result.(*commonResp); ok && r.Code != 0 {
		if r.Code == 401 || r.Code == 403 {
			if err := d.refreshToken(); err != nil {
				return resp, err
			}
			resp, err = d.apiClient.R().SetContext(ctx).SetBody(body).SetResult(result).Post(apiPath)
		}
	}

	return resp, err
}

func (d *GuangYaPan) waitTaskDone(ctx context.Context, taskID string) error {
	for {
		var resp taskStatusResp
		_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_task_status", map[string]interface{}{
			"taskId": taskID,
		}, &resp)
		if err != nil {
			return err
		}
		if resp.Code != 0 {
			return fmt.Errorf("get task status failed: %s", resp.Msg)
		}
		if resp.Data.Status == 2 {
			return nil
		}
		if resp.Data.Status == 3 {
			return fmt.Errorf("task failed")
		}
		time.Sleep(time.Second)
	}
}

func (d *GuangYaPan) getUploadToken(ctx context.Context, parentID, fileName string, fileSize int64) (*uploadTokenData, error) {
	var resp uploadTokenResp
	_, err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_res_center_token", map[string]interface{}{
		"parentId":  parentID,
		"fileName":  fileName,
		"size":      fileSize,
	}, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("get upload token failed: %s", resp.Msg)
	}
	return &resp.Data, nil
}

func (d *GuangYaPan) multipartUploadToOSS(ctx context.Context, token *uploadTokenData, file model.FileStreamer, up driver.UpdateProgress) error {
	endpoint := d.normalizeOSSEndpoint(token.EndPoint)
	if token.FullEndPoint != "" {
		endpoint = d.normalizeOSSEndpoint(token.FullEndPoint)
	}

	client, err := oss.New(endpoint, token.AccessKeyID, token.SecretAccessKey, oss.SecurityToken(token.SessionToken))
	if err != nil {
		return err
	}

	bucket, err := client.Bucket(token.BucketName)
	if err != nil {
		return err
	}

	// For small files, use PutObject directly
	if file.GetSize() <= 100*1024*1024 {
		reader := file
		if up != nil {
			reader = driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
				Reader:         file,
				UpdateProgress: up,
			})
		}
		err = bucket.PutObject(token.ObjectPath, reader)
		if err != nil {
			return err
		}
	} else {
		// For large files, use multipart upload via temp file
		// Create temp file
		tempFile, err := ioutil.TempFile("", "guangyapan-upload-*")
		if err != nil {
			return err
		}
		defer os.Remove(tempFile.Name())
		defer tempFile.Close()

		// Copy stream to temp file
		_, err = io.Copy(tempFile, file)
		if err != nil {
			return err
		}

		// Upload using UploadFile (handles multipart automatically)
		err = bucket.UploadFile(token.ObjectPath, tempFile.Name(), 100*1024*1024)
		if err != nil {
			return err
		}
	}

	var taskResp taskInfoResp
	_, err = d.postAPI(ctx, "/nd.bizuserres.s/v1/file/get_info_by_task_id", map[string]interface{}{
		"taskId": token.TaskID,
	}, &taskResp)
	if err != nil {
		return err
	}
	if taskResp.Code != 0 {
		return fmt.Errorf("upload complete failed: %s", taskResp.Msg)
	}

	return nil
}

func (d *GuangYaPan) normalizeOSSEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	// Remove trailing slash
	raw = strings.TrimSuffix(raw, "/")
	return raw
}

func (d *GuangYaPan) setTempStatus(msg string) {
	log.Infof("GuangYaPan: %s", msg)
}

var _ driver.Driver = (*GuangYaPan)(nil)
