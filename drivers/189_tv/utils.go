package _189_tv

import (
	"context"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skip2/go-qrcode"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	jsoniter "github.com/json-iterator/go"
	"github.com/pkg/errors"
)

const (
	TVAppKey            = "600100885"
	TVAppSignatureSecre = "fe5734c74c2f96a38157f420b32dc995"
	TvVersion           = "6.5.5"
	AndroidTV           = "FAMILY_TV"
	TvChannelId         = "home02"

	ApiUrl = "https://api.cloud.189.cn"
)

func (y *Cloud189TV) SignatureHeader(url, method string, isFamily bool) map[string]string {
	dateOfGmt := getHttpDateStr()
	sessionKey := y.tokenInfo.SessionKey
	sessionSecret := y.tokenInfo.SessionSecret
	if isFamily {
		sessionKey = y.tokenInfo.FamilySessionKey
		sessionSecret = y.tokenInfo.FamilySessionSecret
	}

	header := map[string]string{
		"Date":         dateOfGmt,
		"SessionKey":   sessionKey,
		"X-Request-ID": uuid.NewString(),
		"Signature":    SessionKeySignatureOfHmac(sessionSecret, sessionKey, method, url, dateOfGmt),
	}
	return header
}

func (y *Cloud189TV) AppKeySignatureHeader(url, method string) map[string]string {
	tempTime := timestamp()
	header := map[string]string{
		"Timestamp":    strconv.FormatInt(tempTime, 10),
		"X-Request-ID": uuid.NewString(),
		"AppKey":       TVAppKey,
		"AppSignature": AppKeySignatureOfHmac(TVAppSignatureSecre, TVAppKey, method, url, tempTime),
	}
	return header
}

func (y *Cloud189TV) request(url, method string, callback base.ReqCallback, params map[string]string, resp interface{}, isFamily ...bool) ([]byte, error) {
	req := y.client.R().SetQueryParams(clientSuffix())

	if params != nil {
		req.SetQueryParams(params)
	}

	// Signature
	req.SetHeaders(y.SignatureHeader(url, method, isBool(isFamily...)))

	var erron RespErr
	req.SetError(&erron)

	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}

	if strings.Contains(res.String(), "userSessionBO is null") ||
		strings.Contains(res.String(), "InvalidSessionKey") {
		return nil, errors.New("session expired")
	}

	// 处理错误
	if erron.HasError() {
		return nil, &erron
	}
	return res.Body(), nil
}

func (y *Cloud189TV) get(url string, callback base.ReqCallback, resp interface{}, isFamily ...bool) ([]byte, error) {
	return y.request(url, http.MethodGet, callback, nil, resp, isFamily...)
}

func (y *Cloud189TV) post(url string, callback base.ReqCallback, resp interface{}, isFamily ...bool) ([]byte, error) {
	return y.request(url, http.MethodPost, callback, nil, resp, isFamily...)
}

func (y *Cloud189TV) put(ctx context.Context, url string, headers map[string]string, sign bool, file io.Reader, isFamily bool) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, file)
	if err != nil {
		return nil, err
	}

	query := req.URL.Query()
	for key, value := range clientSuffix() {
		query.Add(key, value)
	}
	req.URL.RawQuery = query.Encode()

	for key, value := range headers {
		req.Header.Add(key, value)
	}

	if sign {
		for key, value := range y.SignatureHeader(url, http.MethodPut, isFamily) {
			req.Header.Add(key, value)
		}
	}

	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var erron RespErr
	jsoniter.Unmarshal(body, &erron)
	xml.Unmarshal(body, &erron)
	if erron.HasError() {
		return nil, &erron
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("put fail,err:%s", string(body))
	}
	return body, nil
}
func (y *Cloud189TV) getFiles(ctx context.Context, fileId string, isFamily bool) ([]model.Obj, error) {
	fullUrl := ApiUrl
	if isFamily {
		fullUrl += "/family/file"
	}
	fullUrl += "/listFiles.action"

	res := make([]model.Obj, 0, 130)
	for pageNum := 1; ; pageNum++ {
		var resp Cloud189FilesResp
		_, err := y.get(fullUrl, func(r *resty.Request) {
			r.SetContext(ctx)
			r.SetQueryParams(map[string]string{
				"folderId":   fileId,
				"fileType":   "0",
				"mediaAttr":  "0",
				"iconOption": "5",
				"pageNum":    fmt.Sprint(pageNum),
				"pageSize":   "130",
			})
			if isFamily {
				r.SetQueryParams(map[string]string{
					"familyId":   y.FamilyID,
					"orderBy":    toFamilyOrderBy(y.OrderBy),
					"descending": toDesc(y.OrderDirection),
				})
			} else {
				r.SetQueryParams(map[string]string{
					"recursive":  "0",
					"orderBy":    y.OrderBy,
					"descending": toDesc(y.OrderDirection),
				})
			}
		}, &resp, isFamily)
		if err != nil {
			return nil, err
		}
		// 获取完毕跳出
		if resp.FileListAO.Count == 0 {
			break
		}

		for i := 0; i < len(resp.FileListAO.FolderList); i++ {
			res = append(res, &resp.FileListAO.FolderList[i])
		}
		for i := 0; i < len(resp.FileListAO.FileList); i++ {
			res = append(res, &resp.FileListAO.FileList[i])
		}
	}
	return res, nil
}

func (y *Cloud189TV) login() (err error) {
	req := y.client.R().SetQueryParams(clientSuffix())
	var erron RespErr
	var tokenInfo AppSessionResp
	if y.Addition.AccessToken == "" {
		if y.Addition.TempUuid == "" {
			// 获取登录参数
			var uuidInfo UuidInfoResp
			req.SetResult(&uuidInfo).SetError(&erron)
			// Signature
			req.SetHeaders(y.AppKeySignatureHeader(ApiUrl+"/family/manage/getQrCodeUUID.action",
				http.MethodGet))
			_, err = req.Execute(http.MethodGet, ApiUrl+"/family/manage/getQrCodeUUID.action")

			if err != nil {
				return
			}
			if erron.HasError() {
				return &erron
			}

			if uuidInfo.Uuid == "" {
				return errors.New("uuidInfo is empty")
			}
			y.Addition.TempUuid = uuidInfo.Uuid
			op.MustSaveDriverStorage(y)

			// 展示二维码
			qrTemplate := `<body>
    <img src="data:image/jpeg;base64,%s"/>
    <br>Or Click here: <a href="%s">%s</a>
</body>`

			// Generate QR code
			qrCode, err := qrcode.Encode(uuidInfo.Uuid, qrcode.Medium, 256)
			if err != nil {
				return fmt.Errorf("failed to generate QR code: %v", err)
			}

			// Encode QR code to base64
			qrCodeBase64 := base64.StdEncoding.EncodeToString(qrCode)

			// Create the HTML page
			qrPage := fmt.Sprintf(qrTemplate, qrCodeBase64, uuidInfo.Uuid, uuidInfo.Uuid)
			return fmt.Errorf("need verify: \n%s", qrPage)

		} else {
			var accessTokenResp E189AccessTokenResp
			req.SetResult(&accessTokenResp).SetError(&erron)
			// Signature
			req.SetHeaders(y.AppKeySignatureHeader(ApiUrl+"/family/manage/qrcodeLoginResult.action",
				http.MethodGet))
			req.SetQueryParam("uuid", y.Addition.TempUuid)
			_, err = req.Execute(http.MethodGet, ApiUrl+"/family/manage/qrcodeLoginResult.action")
			if err != nil {
				return
			}
			if erron.HasError() {
				return &erron
			}
			if accessTokenResp.E189AccessToken == "" {
				return errors.New("E189AccessToken is empty")
			}
			y.Addition.AccessToken = accessTokenResp.E189AccessToken
			y.Addition.TempUuid = ""
		}
	}
	// 获取SessionKey 和 SessionSecret
	reqb := y.client.R().SetQueryParams(clientSuffix())
	reqb.SetResult(&tokenInfo).SetError(&erron)
	// Signature
	reqb.SetHeaders(y.AppKeySignatureHeader(ApiUrl+"/family/manage/loginFamilyMerge.action",
		http.MethodGet))
	reqb.SetQueryParam("e189AccessToken", y.Addition.AccessToken)
	_, err = reqb.Execute(http.MethodGet, ApiUrl+"/family/manage/loginFamilyMerge.action")
	if err != nil {
		return
	}

	if erron.HasError() {
		return &erron
	}

	y.tokenInfo = &tokenInfo
	op.MustSaveDriverStorage(y)
	return
}

func (y *Cloud189TV) RapidUpload(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, isFamily bool, overwrite bool) (model.Obj, error) {
	fileMd5 := stream.GetHash().GetHash(utils.MD5)
	if len(fileMd5) < utils.MD5.Width {
		return nil, errors.New("invalid hash")
	}

	uploadInfo, err := y.OldUploadCreate(ctx, dstDir.GetID(), fileMd5, stream.GetName(), fmt.Sprint(stream.GetSize()), isFamily)
	if err != nil {
		return nil, err
	}

	if uploadInfo.FileDataExists != 1 {
		return nil, errors.New("rapid upload fail")
	}

	return y.OldUploadCommit(ctx, uploadInfo.FileCommitUrl, uploadInfo.UploadFileId, isFamily, overwrite)
}

// 旧版本上传，家庭云不支持覆盖
func (y *Cloud189TV) OldUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress, isFamily bool, overwrite bool) (model.Obj, error) {
	fileMd5 := file.GetHash().GetHash(utils.MD5)
	var tempFile = file.GetFile()
	var err error
	if len(fileMd5) != utils.MD5.Width {
		tempFile, fileMd5, err = stream.CacheFullAndHash(file, &up, utils.MD5)
	} else if tempFile == nil {
		tempFile, err = file.CacheFullAndWriter(&up, nil)
	}
	if err != nil {
		return nil, err
	}

	// 创建上传会话
	uploadInfo, err := y.OldUploadCreate(ctx, dstDir.GetID(), fileMd5, file.GetName(), fmt.Sprint(file.GetSize()), isFamily)
	if err != nil {
		return nil, err
	}

	// 网盘中不存在该文件，开始上传
	status := GetUploadFileStatusResp{CreateUploadFileResp: *uploadInfo}
	for status.GetSize() < file.GetSize() && status.FileDataExists != 1 {
		if utils.IsCanceled(ctx) {
			return nil, ctx.Err()
		}

		header := map[string]string{
			"ResumePolicy": "1",
			"Expect":       "100-continue",
		}

		if isFamily {
			header["FamilyId"] = fmt.Sprint(y.FamilyID)
			header["UploadFileId"] = fmt.Sprint(status.UploadFileId)
		} else {
			header["Edrive-UploadFileId"] = fmt.Sprint(status.UploadFileId)
		}

		_, err := y.put(ctx, status.FileUploadUrl, header, true, tempFile, isFamily)
		if err, ok := err.(*RespErr); ok && err.Code != "InputStreamReadError" {
			return nil, err
		}

		// 获取断点状态
		fullUrl := ApiUrl + "/getUploadFileStatus.action"
		if y.isFamily() {
			fullUrl = ApiUrl + "/family/file/getFamilyFileStatus.action"
		}
		_, err = y.get(fullUrl, func(req *resty.Request) {
			req.SetContext(ctx).SetQueryParams(map[string]string{
				"uploadFileId": fmt.Sprint(status.UploadFileId),
				"resumePolicy": "1",
			})
			if isFamily {
				req.SetQueryParam("familyId", fmt.Sprint(y.FamilyID))
			}
		}, &status, isFamily)
		if err != nil {
			return nil, err
		}
		if _, err := tempFile.Seek(status.GetSize(), io.SeekStart); err != nil {
			return nil, err
		}
		up(float64(status.GetSize()) / float64(file.GetSize()) * 100)
	}

	return y.OldUploadCommit(ctx, status.FileCommitUrl, status.UploadFileId, isFamily, overwrite)
}

// 创建上传会话
func (y *Cloud189TV) OldUploadCreate(ctx context.Context, parentID string, fileMd5, fileName, fileSize string, isFamily bool) (*CreateUploadFileResp, error) {
	var uploadInfo CreateUploadFileResp

	fullUrl := ApiUrl + "/createUploadFile.action"
	if isFamily {
		fullUrl = ApiUrl + "/family/file/createFamilyFile.action"
	}
	_, err := y.post(fullUrl, func(req *resty.Request) {
		req.SetContext(ctx)
		if isFamily {
			req.SetQueryParams(map[string]string{
				"familyId":     y.FamilyID,
				"parentId":     parentID,
				"fileMd5":      fileMd5,
				"fileName":     fileName,
				"fileSize":     fileSize,
				"resumePolicy": "1",
			})
		} else {
			req.SetFormData(map[string]string{
				"parentFolderId": parentID,
				"fileName":       fileName,
				"size":           fileSize,
				"md5":            fileMd5,
				"opertype":       "3",
				"flag":           "1",
				"resumePolicy":   "1",
				"isLog":          "0",
			})
		}
	}, &uploadInfo, isFamily)

	if err != nil {
		return nil, err
	}
	return &uploadInfo, nil
}

// 提交上传文件
func (y *Cloud189TV) OldUploadCommit(ctx context.Context, fileCommitUrl string, uploadFileID int64, isFamily bool, overwrite bool) (model.Obj, error) {
	var resp OldCommitUploadFileResp
	_, err := y.post(fileCommitUrl, func(req *resty.Request) {
		req.SetContext(ctx)
		if isFamily {
			req.SetHeaders(map[string]string{
				"ResumePolicy": "1",
				"UploadFileId": fmt.Sprint(uploadFileID),
				"FamilyId":     fmt.Sprint(y.FamilyID),
			})
		} else {
			req.SetFormData(map[string]string{
				"opertype":     IF(overwrite, "3", "1"),
				"resumePolicy": "1",
				"uploadFileId": fmt.Sprint(uploadFileID),
				"isLog":        "0",
			})
		}
	}, &resp, isFamily)
	if err != nil {
		return nil, err
	}
	return resp.toFile(), nil
}

func (y *Cloud189TV) isFamily() bool {
	return y.Type == "family"
}

func (y *Cloud189TV) isLogin() bool {
	if y.tokenInfo == nil {
		return false
	}
	_, err := y.get(ApiUrl+"/getUserInfo.action", nil, nil)
	return err == nil
}

// 获取家庭云所有用户信息
func (y *Cloud189TV) getFamilyInfoList() ([]FamilyInfoResp, error) {
	var resp FamilyInfoListResp
	_, err := y.get(ApiUrl+"/family/manage/getFamilyList.action", nil, &resp, true)
	if err != nil {
		return nil, err
	}
	return resp.FamilyInfoResp, nil
}

// 抽取家庭云ID
func (y *Cloud189TV) getFamilyID() (string, error) {
	infos, err := y.getFamilyInfoList()
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return "", fmt.Errorf("cannot get automatically,please input family_id")
	}
	for _, info := range infos {
		if strings.Contains(y.tokenInfo.LoginName, info.RemarkName) {
			return fmt.Sprint(info.FamilyID), nil
		}
	}
	return fmt.Sprint(infos[0].FamilyID), nil
}

func (y *Cloud189TV) CreateBatchTask(aType string, familyID string, targetFolderId string, other map[string]string, taskInfos ...BatchTaskInfo) (*CreateBatchTaskResp, error) {
	var resp CreateBatchTaskResp
	_, err := y.post(ApiUrl+"/batch/createBatchTask.action", func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"type":      aType,
			"taskInfos": MustString(utils.Json.MarshalToString(taskInfos)),
		})
		if targetFolderId != "" {
			req.SetFormData(map[string]string{"targetFolderId": targetFolderId})
		}
		if familyID != "" {
			req.SetFormData(map[string]string{"familyId": familyID})
		}
		req.SetFormData(other)
	}, &resp, familyID != "")
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// 检测任务状态
func (y *Cloud189TV) CheckBatchTask(aType string, taskID string) (*BatchTaskStateResp, error) {
	var resp BatchTaskStateResp
	_, err := y.post(ApiUrl+"/batch/checkBatchTask.action", func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"type":   aType,
			"taskId": taskID,
		})
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// 获取冲突的任务信息
func (y *Cloud189TV) GetConflictTaskInfo(aType string, taskID string) (*BatchTaskConflictTaskInfoResp, error) {
	var resp BatchTaskConflictTaskInfoResp
	_, err := y.post(ApiUrl+"/batch/getConflictTaskInfo.action", func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"type":   aType,
			"taskId": taskID,
		})
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// 处理冲突
func (y *Cloud189TV) ManageBatchTask(aType string, taskID string, targetFolderId string, taskInfos ...BatchTaskInfo) error {
	_, err := y.post(ApiUrl+"/batch/manageBatchTask.action", func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"targetFolderId": targetFolderId,
			"type":           aType,
			"taskId":         taskID,
			"taskInfos":      MustString(utils.Json.MarshalToString(taskInfos)),
		})
	}, nil)
	return err
}

var ErrIsConflict = errors.New("there is a conflict with the target object")

// 等待任务完成
func (y *Cloud189TV) WaitBatchTask(aType string, taskID string, t time.Duration) error {
	for {
		state, err := y.CheckBatchTask(aType, taskID)
		if err != nil {
			return err
		}
		switch state.TaskStatus {
		case 2:
			return ErrIsConflict
		case 4:
			return nil
		}
		time.Sleep(t)
	}
}
