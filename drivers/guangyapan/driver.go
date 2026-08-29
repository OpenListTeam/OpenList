package guangyapan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const (
	accountBaseURL = "https://account.guangyapan.com"
	apiBaseURL     = "https://api.guangyapan.com"
)

type GuangYaPan struct {
	model.Storage
	Addition

	accountClient *resty.Client
	apiClient     *resty.Client

	resolvedRootFolderID string
	rootFolderResolved   bool

	// refreshMu protects concurrent access to AccessToken/RefreshToken during refresh.
	refreshMu sync.Mutex
	// statusTimer tracks delayed status updates for cancellation on Drop.
	statusTimer *time.Timer

	// apiRateLimit throttles requests per API endpoint so that batch operations
	// (e.g. copying many files cross-storage) don't flood the upstream API.
	apiRateLimit sync.Map
}

// apiRateInterval is the minimum gap between two requests to the same endpoint.
const apiRateInterval = 500 * time.Millisecond

func (d *GuangYaPan) Config() driver.Config {
	return config
}

func (d *GuangYaPan) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *GuangYaPan) Init(ctx context.Context) error {
	d.ClientID = strings.TrimSpace(d.ClientID)
	if d.ClientID == "" {
		return errors.New("client_id is required, please provide a valid client_id")
	}
	d.DeviceID = normalizeDeviceID(d.DeviceID)
	if d.DeviceID == "" {
		d.DeviceID = randomDeviceID()
	}
	deviceSign := strings.TrimSpace(d.DeviceSign)
	if deviceSign == "" {
		deviceSign = "wdi10." + d.DeviceID
	}
	if d.PageSize <= 0 {
		d.PageSize = 100
	}
	if d.OrderBy < 0 {
		d.OrderBy = 3
	}
	if d.SortType != 0 && d.SortType != 1 {
		d.SortType = 1
	}

	d.RootPath = strings.TrimSpace(d.RootPath)
	d.AccessToken = strings.TrimSpace(d.AccessToken)
	d.RefreshToken = strings.TrimSpace(d.RefreshToken)
	d.PhoneNumber = strings.TrimSpace(d.PhoneNumber)
	d.VerifyCode = strings.TrimSpace(d.VerifyCode)
	d.CaptchaToken = strings.TrimSpace(d.CaptchaToken)
	d.VerificationID = strings.TrimSpace(d.VerificationID)
	d.resolvedRootFolderID = ""
	d.rootFolderResolved = false

	d.accountClient = base.NewRestyClient().
		SetBaseURL(accountBaseURL).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Content-Type", "application/json").
		SetHeader("X-Device-Model", "chrome%2F147.0.0.0").
		SetHeader("X-Device-Name", "PC-Chrome").
		SetHeader("X-Device-Sign", deviceSign).
		SetHeader("X-Net-Work-Type", "NONE").
		SetHeader("X-OS-Version", "MacIntel").
		SetHeader("X-Platform-Version", "1").
		SetHeader("X-Protocol-Version", "301").
		SetHeader("X-Provider-Name", "NONE").
		SetHeader("X-SDK-Version", "9.0.2").
		SetHeader("X-Client-Id", d.ClientID).
		SetHeader("X-Client-Version", "0.0.1").
		SetHeader("X-Device-Id", d.DeviceID)

	d.apiClient = base.NewRestyClient().
		SetBaseURL(apiBaseURL).
		SetHeader("Accept", "application/json, text/plain, */*").
		SetHeader("Content-Type", "application/json").
		SetHeader("Did", d.DeviceID).
		SetHeader("Dt", "4")

	// Priority: access_token -> refresh_token -> sms login.
	if d.AccessToken != "" {
		if err := d.validateToken(ctx); err == nil {
			return d.prepareRootFolder(ctx)
		}
		d.AccessToken = ""
	}
	if d.RefreshToken != "" {
		if err := d.refreshToken(ctx); err == nil {
			if err2 := d.validateToken(ctx); err2 == nil {
				return d.prepareRootFolder(ctx)
			}
		}
	}
	// Two-stage SMS flow:
	// 1) phone only + send_code=true: send code and cache verification_id (do not fail init).
	// 2) phone + verify_code: complete login and save tokens.
	if d.PhoneNumber != "" {
		if d.canSMSLogin() {
			if err := d.loginBySMSCode(ctx); err != nil {
				return err
			}
			if err := d.validateToken(ctx); err != nil {
				return err
			}
			return d.prepareRootFolder(ctx)
		}
		if d.SendCode {
			d.setTempStatus("SMS sending in progress...")
			if err := d.prepareSMSCode(ctx); err != nil {
				d.setTempStatus(fmt.Sprintf("SMS send failed: %v. Please check captcha/meta and set send_code=true to retry.", err))
				log.Warnf("guangyapan: prepare sms code failed: %v", err)
			} else {
				d.setTempStatus("SMS sent successfully. Please fill verify_code and save to complete login.")
			}
		}
		return nil
	}
	return errors.New("login failed: provide a valid access_token, or refresh_token, or phone_number + verify_code + captcha_token")
}

func (d *GuangYaPan) Drop(ctx context.Context) error {
	if d.statusTimer != nil {
		d.statusTimer.Stop()
	}
	return nil
}

func (d *GuangYaPan) GetRoot(ctx context.Context) (model.Obj, error) {
	rootID, err := d.getRootFolderID(ctx)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       rootID,
		Path:     "/",
		Name:     "root",
		Size:     0,
		Modified: d.Modified,
		IsFolder: true,
	}, nil
}

func (d *GuangYaPan) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if err := d.ensureAccessToken(ctx); err != nil {
		return nil, err
	}

	parentID := dir.GetID()

	const maxPage = 10000
	res := make([]model.Obj, 0, d.PageSize)
	for page := 0; page < maxPage; page++ {
		var resp listResp
		body := map[string]any{
			"parentId":  parentID,
			"page":      page,
			"pageSize":  d.PageSize,
			"orderBy":   d.OrderBy,
			"sortType":  d.SortType,
		}
		if err := d.postAPI(ctx, "/userres/v1/file/get_file_list", body, &resp); err != nil {
			return nil, err
		}
		for _, item := range resp.Data.List {
			res = append(res, &model.Object{
				ID:       item.FileID,
				Path:     parentID,
				Name:     item.FileName,
				Size:     item.FileSize,
				Modified: unixOrZero(item.UTime),
				Ctime:    unixOrZero(item.CTime),
				IsFolder: item.ResType == 2,
			})
		}
		if len(resp.Data.List) < d.PageSize {
			break
		}
		if resp.Data.Total > 0 && len(res) >= resp.Data.Total {
			break
		}
	}
	return res, nil
}

func (d *GuangYaPan) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	if err := d.ensureAccessToken(ctx); err != nil {
		return nil, err
	}

	var resp downloadResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/get_res_download_url", map[string]any{
		"fileId": file.GetID(),
	}, &resp); err != nil {
		return nil, err
	}

	url := strings.TrimSpace(resp.Data.SignedURL)
	if url == "" {
		url = strings.TrimSpace(resp.Data.DownloadURL)
	}
	if url == "" {
		return nil, errors.New("empty download url")
	}
	return &model.Link{URL: url}, nil
}

func (d *GuangYaPan) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	if err := d.ensureAccessToken(ctx); err != nil {
		return err
	}

	name := strings.TrimSpace(dirName)
	if name == "" {
		return errors.New("dir name is empty")
	}

	parentID := parentDir.GetID()

	var out createDirResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/create_dir", map[string]any{
		"parentId": parentID,
		"dirName":  name,
	}, &out); err != nil {
		return err
	}
	if !isSuccessMsg(out.Msg) {
		return fmt.Errorf("make dir failed: %s", strings.TrimSpace(out.Msg))
	}
	return nil
}

func (d *GuangYaPan) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if err := d.ensureAccessToken(ctx); err != nil {
		return err
	}

	fileID := strings.TrimSpace(srcObj.GetID())
	if fileID == "" {
		return errors.New("file id is empty")
	}
	name := strings.TrimSpace(newName)
	if name == "" {
		return errors.New("new name is empty")
	}

	var out commonResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/rename", map[string]any{
		"fileId":  fileID,
		"newName": name,
	}, &out); err != nil {
		return err
	}
	if !isSuccessMsg(out.Msg) {
		return fmt.Errorf("rename failed: %s", strings.TrimSpace(out.Msg))
	}
	return nil
}

func (d *GuangYaPan) Remove(ctx context.Context, obj model.Obj) error {
	if err := d.ensureAccessToken(ctx); err != nil {
		return err
	}

	fileID := strings.TrimSpace(obj.GetID())
	if fileID == "" {
		return errors.New("file id is empty")
	}

	var del taskResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/delete_file", map[string]any{
		"fileIds": []string{fileID},
	}, &del); err != nil {
		return err
	}
	if !isSuccessMsg(del.Msg) {
		return fmt.Errorf("delete failed: %s", strings.TrimSpace(del.Msg))
	}

	taskID := strings.TrimSpace(del.Data.TaskID)
	if taskID == "" {
		// Some backends may apply deletion synchronously.
		return nil
	}
	return d.waitTaskDone(ctx, taskID)
}

func (d *GuangYaPan) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	if err := d.ensureAccessToken(ctx); err != nil {
		return err
	}

	fileID := strings.TrimSpace(srcObj.GetID())
	if fileID == "" {
		return errors.New("file id is empty")
	}
	parentID := dstDir.GetID()

	var out taskResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/move_file", map[string]any{
		"fileIds":  []string{fileID},
		"parentId": parentID,
	}, &out); err != nil {
		return err
	}
	if !isSuccessMsg(out.Msg) {
		return fmt.Errorf("move failed: %s", strings.TrimSpace(out.Msg))
	}
	taskID := strings.TrimSpace(out.Data.TaskID)
	if taskID == "" {
		return nil
	}
	return d.waitTaskDone(ctx, taskID)
}

func (d *GuangYaPan) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	if err := d.ensureAccessToken(ctx); err != nil {
		return err
	}

	fileID := strings.TrimSpace(srcObj.GetID())
	if fileID == "" {
		return errors.New("file id is empty")
	}
	parentID := dstDir.GetID()

	var out taskResp
	if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/copy_file", map[string]any{
		"fileIds":  []string{fileID},
		"parentId": parentID,
	}, &out); err != nil {
		return err
	}
	if !isSuccessMsg(out.Msg) {
		return fmt.Errorf("copy failed: %s", strings.TrimSpace(out.Msg))
	}
	taskID := strings.TrimSpace(out.Data.TaskID)
	if taskID == "" {
		return nil
	}
	return d.waitTaskDone(ctx, taskID)
}

func (d *GuangYaPan) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if err := d.ensureAccessToken(ctx); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errors.New("file is nil")
	}
	if file.GetSize() < 0 {
		return nil, errors.New("invalid file size")
	}
	name := strings.TrimSpace(file.GetName())
	if name == "" {
		return nil, errors.New("file name is empty")
	}

	parentID := dstDir.GetID()

	token, code, err := d.getUploadToken(ctx, parentID, name, file.GetSize())
	if err != nil {
		return nil, err
	}
	taskID := strings.TrimSpace(token.TaskID)
	// code == 156 (instant upload) or AlreadyDone mean the backend has already
	// finished/imported the file; there is no OSS upload to perform.
	if code == 156 || token.AlreadyDone {
		if taskID == "" {
			return nil, errors.New("instant upload returns empty task id")
		}
		if err := d.waitUploadTaskInfo(ctx, taskID); err != nil {
			return nil, err
		}
		return nil, nil
	}

	if token.ObjectPath == "" || token.BucketName == "" || token.EndPoint == "" || token.AccessKeyID == "" || token.SecretAccessKey == "" {
		return nil, errors.New("upload token is incomplete")
	}

	ossEndpoint := normalizeOSSEndpoint(token.EndPoint, token.BucketName)
	client, err := oss.New(ossEndpoint, token.AccessKeyID, token.SecretAccessKey, oss.SecurityToken(token.SessionToken))
	if err != nil {
		return nil, fmt.Errorf("create oss client failed: %w", err)
	}
	bucket, err := client.Bucket(token.BucketName)
	if err != nil {
		return nil, fmt.Errorf("create oss bucket failed: %w", err)
	}

	if file.GetSize() == 0 {
		if err := bucket.PutObject(token.ObjectPath, strings.NewReader("")); err != nil {
			return nil, err
		}
	} else {
		if err := d.multipartUploadToOSS(ctx, bucket, token.ObjectPath, file, up); err != nil {
			return nil, err
		}
	}

	if taskID == "" {
		return nil, nil
	}
	if err := d.waitUploadTaskInfo(ctx, taskID); err != nil {
		return nil, err
	}
	return nil, nil
}

func (d *GuangYaPan) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	if err := d.ensureAccessToken(ctx); err != nil {
		return nil, err
	}

	var resp assetsInfoResp
	if err := d.postAPI(ctx, "/nd.bizassets.s/v1/get_assets", nil, &resp); err != nil {
		return nil, err
	}
	if resp.IsSuccess() && resp.Data.TotalSpaceSize > 0 {
		return &model.StorageDetails{
			DiskUsage: model.DiskUsage{
				TotalSpace: resp.Data.TotalSpaceSize,
				UsedSpace:  resp.Data.UsedSpaceSize,
			},
		}, nil
	}
	return nil, errors.New("failed to get storage details")
}

func (d *GuangYaPan) getRootFolderID(ctx context.Context) (string, error) {
	if d.rootFolderResolved {
		return d.resolvedRootFolderID, nil
	}
	if err := d.ensureAccessToken(ctx); err != nil {
		return "", err
	}
	if err := d.prepareRootFolder(ctx); err != nil {
		return "", err
	}
	return d.resolvedRootFolderID, nil
}

func (d *GuangYaPan) prepareRootFolder(ctx context.Context) error {
	rootID, err := d.resolveConfiguredRootFolderID(ctx)
	if err != nil {
		return err
	}
	d.resolvedRootFolderID = rootID
	d.rootFolderResolved = true
	return nil
}

func (d *GuangYaPan) resolveConfiguredRootFolderID(ctx context.Context) (string, error) {
	root := strings.TrimSpace(d.RootPath)
	if root == "" {
		return "", nil
	}
	return d.resolveFolderPath(ctx, root)
}

func (d *GuangYaPan) resolveFolderPath(ctx context.Context, rootPath string) (string, error) {
	cleanPath := strings.Trim(strings.ReplaceAll(strings.TrimSpace(rootPath), "\\", "/"), "/")
	if cleanPath == "" {
		return "", nil
	}

	parentID := ""
	for _, name := range strings.Split(cleanPath, "/") {
		if name == "" {
			continue
		}
		childID, err := d.findChildFolderID(ctx, parentID, name)
		if err != nil {
			return "", err
		}
		parentID = childID
	}
	return parentID, nil
}

func (d *GuangYaPan) findChildFolderID(ctx context.Context, parentID, name string) (string, error) {
	pageSize := d.PageSize
	if pageSize <= 0 {
		pageSize = 100
	}

	const maxPage = 10000
	seen := 0
	for page := 0; page < maxPage; page++ {
		var resp listResp
		body := map[string]any{
			"parentId":  parentID,
			"page":      page,
			"pageSize":  pageSize,
			"orderBy":   d.OrderBy,
			"sortType":  d.SortType,
		}
		if err := d.postAPI(ctx, "/nd.bizuserres.s/v1/file/get_file_list", body, &resp); err != nil {
			return "", err
		}
		for _, item := range resp.Data.List {
			seen++
			if item.ResType == 2 && item.FileName == name {
				return item.FileID, nil
			}
		}
		if len(resp.Data.List) < pageSize {
			break
		}
		if resp.Data.Total > 0 && seen >= resp.Data.Total {
			break
		}
	}

	if parentID == "" {
		return "", fmt.Errorf("resolve root folder path failed: folder %q not found under /", name)
	}
	return "", fmt.Errorf("resolve root folder path failed: folder %q not found under parent %s", name, parentID)
}

func (d *GuangYaPan) ensureAccessToken(ctx context.Context) error {
	if strings.TrimSpace(d.AccessToken) != "" {
		return nil
	}
	if strings.TrimSpace(d.RefreshToken) == "" {
		return errors.New("not logged in, please re-init storage")
	}
	return d.refreshToken(ctx)
}

func (d *GuangYaPan) validateToken(ctx context.Context) error {
	var me userMeResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+d.AccessToken).
		SetResult(&me).
		Get("/v1/user/me")
	if err != nil {
		return err
	}
	if resp.IsError() {
		return fmt.Errorf("validate token failed: status=%d body=%s", resp.StatusCode(), resp.String())
	}
	if strings.TrimSpace(me.Sub) == "" {
		return errors.New("validate token failed: empty user sub")
	}
	return nil
}

func (d *GuangYaPan) refreshToken(ctx context.Context) error {
	if strings.TrimSpace(d.RefreshToken) == "" {
		return errors.New("refresh_token is empty")
	}

	d.refreshMu.Lock()
	defer d.refreshMu.Unlock()

	// Double-check after acquiring lock (may have been refreshed by another goroutine)
	if strings.TrimSpace(d.AccessToken) != "" {
		if err := d.validateToken(ctx); err == nil {
			return nil
		}
	}

	var out tokenResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"client_id":     d.ClientID,
			"grant_type":    "refresh_token",
			"refresh_token": d.RefreshToken,
		}).
		SetResult(&out).
		Post("/v1/auth/token")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		errMsg := strings.TrimSpace(out.ErrorDesc)
		if errMsg == "" {
			errMsg = strings.TrimSpace(out.Error)
		}
		if errMsg == "" {
			errMsg = strings.TrimSpace(resp.String())
		}
		if errMsg == "" {
			errMsg = fmt.Sprintf("status=%d", resp.StatusCode())
		}
		return fmt.Errorf("refresh token failed: %s", errMsg)
	}

	d.AccessToken = strings.TrimSpace(out.AccessToken)
	if strings.TrimSpace(out.RefreshToken) != "" {
		d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	}
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *GuangYaPan) canSMSLogin() bool {
	return d.PhoneNumber != "" && d.VerifyCode != ""
}

func (d *GuangYaPan) loginBySMSCode(ctx context.Context) error {
	verificationID := strings.TrimSpace(d.VerificationID)
	if verificationID == "" {
		var err error
		verificationID, err = d.requestVerificationID(ctx)
		if err != nil {
			return err
		}
	}

	var step2 verifyResp
	resp, err := d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_id":   verificationID,
			"verification_code": d.VerifyCode,
			"client_id":         d.ClientID,
		}).
		SetResult(&step2).
		Post("/v1/auth/verification/verify")
	if err != nil {
		return err
	}
	if resp.IsError() || step2.Error != "" || strings.TrimSpace(step2.VerificationToken) == "" {
		return fmt.Errorf("verify code failed: %s", d.accountErr(step2.ErrorDesc, step2.Error, resp))
	}

	var out tokenResp
	resp, err = d.accountClient.R().
		SetContext(ctx).
		SetBody(map[string]any{
			"verification_code":  d.VerifyCode,
			"verification_token": step2.VerificationToken,
			"username":           normalizePhoneE164(d.PhoneNumber),
			"client_id":          d.ClientID,
		}).
		SetResult(&out).
		Post("/v1/auth/signin")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.AccessToken) == "" {
		return fmt.Errorf("signin failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}

	d.AccessToken = strings.TrimSpace(out.AccessToken)
	d.RefreshToken = strings.TrimSpace(out.RefreshToken)
	d.VerificationID = ""
	// One-time SMS code should not be reused after successful login.
	d.VerifyCode = ""
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *GuangYaPan) prepareSMSCode(ctx context.Context) error {
	// Explicit send action should always refresh verification_id.
	d.VerificationID = ""
	if err := d.ensureCaptchaToken(ctx, false); err != nil {
		return err
	}
	verificationID, err := d.requestVerificationID(ctx)
	if err != nil {
		return err
	}
	d.VerificationID = verificationID
	d.SendCode = false
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *GuangYaPan) requestVerificationID(ctx context.Context) (string, error) {
	req := d.accountClient.R().SetContext(ctx)
	if d.CaptchaToken != "" {
		req.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}

	var step1 verificationResp
	resp, err := req.
		SetBody(map[string]any{
			"phone_number": normalizePhoneE164(d.PhoneNumber),
			"target":       "ANY",
			"client_id":    d.ClientID,
		}).
		SetResult(&step1).
		Post("/v1/auth/verification")
	if err != nil {
		return "", err
	}
	if resp.IsError() || step1.Error != "" || strings.TrimSpace(step1.VerificationID) == "" {
		// If captcha token is expired/invalid, refresh it once and retry.
		if strings.Contains(step1.Error, "captcha_invalid") || strings.Contains(step1.ErrorDesc, "captcha_token expired") {
			if err := d.ensureCaptchaToken(ctx, true); err == nil {
				return d.requestVerificationID(ctx)
			}
		}
		return "", fmt.Errorf("request verification failed: %s", d.accountErr(step1.ErrorDesc, step1.Error, resp))
	}
	return strings.TrimSpace(step1.VerificationID), nil
}

func (d *GuangYaPan) ensureCaptchaToken(ctx context.Context, force bool) error {
	if !force && d.CaptchaToken != "" {
		return nil
	}

	var out captchaInitResp
	req := d.accountClient.R().SetContext(ctx)
	if d.CaptchaToken != "" {
		req.SetHeader("X-Captcha-Token", d.CaptchaToken)
	}
	resp, err := req.
		SetBody(map[string]any{
			"client_id": d.ClientID,
			"action":    "POST:/v1/auth/verification",
			"device_id": d.DeviceID,
			"meta": map[string]any{
				"username":           normalizePhoneE164(d.PhoneNumber),
				"phone_number":       normalizePhoneE164(d.PhoneNumber),
				"VERIFICATION_PHONE": normalizePhoneE164(d.PhoneNumber),
			},
		}).
		SetResult(&out).
		Post("/v1/shield/captcha/init")
	if err != nil {
		return err
	}
	if resp.IsError() || out.Error != "" || strings.TrimSpace(out.CaptchaToken) == "" {
		return fmt.Errorf("init captcha token failed: %s", d.accountErr(out.ErrorDesc, out.Error, resp))
	}
	d.CaptchaToken = strings.TrimSpace(out.CaptchaToken)
	op.MustSaveDriverStorage(d)
	return nil
}

// Interface compliance checks
var (
	_ driver.Driver      = (*GuangYaPan)(nil)
	_ driver.GetRooter   = (*GuangYaPan)(nil)
	_ driver.Mkdir       = (*GuangYaPan)(nil)
	_ driver.Move        = (*GuangYaPan)(nil)
	_ driver.Copy        = (*GuangYaPan)(nil)
	_ driver.Rename      = (*GuangYaPan)(nil)
	_ driver.Remove      = (*GuangYaPan)(nil)
	_ driver.PutResult   = (*GuangYaPan)(nil)
	_ driver.WithDetails = (*GuangYaPan)(nil)
)
