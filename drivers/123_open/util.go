package _123_open

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const (
	baseURL = "https://open-api.123pan.com"
	// 个人开发者获取token qps 1
	accessTokenAPI = "/api/v1/access_token"
	// 第三方授权应用获取授权 qps 0
	refreshTokenAPI = "/api/v1/oauth2/access_token"
	// 获取用户信息 qps 1
	userInfoAPI = "/api/v1/user/info"
	// 获取文件列表 qps 3
	fileListAPI = "/api/v2/file/list"
	// 获取下载信息 qps 0
	downloadInfoAPI = "/api/v1/file/download_info"
	// 创建文件夹 qps 2
	mkdirAPI = "/upload/v1/file/mkdir"
	// 移动文件 qps 1
	moveAPI = "/api/v1/file/move"
	// 重命名文件 qps 0
	renameAPI = "/api/v1/file/rename"
	// 删除到回收站 qps 0
	trashAPI = "/api/v1/file/trash"
	// 预上传文件 qps 0
	preupCreateAPI = "/upload/v2/file/create"
	// 分片上传 qps 0 （注意：这个接口的baseURL是preupCreateAPI接口返回的，不是上面的baseURL）
	sliceUploadAPI = "/upload/v2/file/slice"
	// 分片上传完成 qps 0
	uploadCompleteAPI = "/upload/v2/file/upload_complete"
	// 获取上传地址（单步上传使用） qps 0
	uploadURLAPI = "/upload/v2/file/domain"
	// 单步上传 qps 0 （注意：这个接口的baseURL是uploadURLAPI接口返回的，不是上面的baseURL）
	singleUploadAPI = "/upload/v2/file/single/create"

	// 分片上传v1版本接口------------------------------------------------

	// 预上传v1 qps 2
	preupCreateV1API = "/upload/v1/file/create"
	// 预上传获取上传地址 qps 0
	getUploadURLAPI = "/upload/v1/file/get_upload_url"
	// 分片上传完毕 qps 0
	uploadCompleteV1API = "/upload/v1/file/upload_complete"
	// 异步轮询获取上传结果 qps 0
	uploadAsyncResultAPI = "/upload/v1/file/upload_async_result"
	// 列举已上传分片
	listUploadedPartsAPI = "/upload/v1/file/list_uploaded_parts"
)

// 个人开发者QPS限制
var idQPSLimit = map[string]int{
	accessTokenAPI:       1,
	userInfoAPI:          1,
	fileListAPI:          3,
	downloadInfoAPI:      0,
	mkdirAPI:             0,
	moveAPI:              1,
	renameAPI:            0,
	trashAPI:             0,
	preupCreateAPI:       0,
	sliceUploadAPI:       0,
	uploadCompleteAPI:    0,
	uploadURLAPI:         0,
	singleUploadAPI:      0,
	preupCreateV1API:     0,
	getUploadURLAPI:      0,
	uploadCompleteV1API:  0,
	uploadAsyncResultAPI: 0,
	listUploadedPartsAPI: 0,
	refreshTokenAPI:      0,
}

// 第三方授权应用QPS限制
var appQPSLimit = map[string]int{
	preupCreateV1API:     5,
	getUploadURLAPI:      20,
	listUploadedPartsAPI: 20,
	mkdirAPI:             5,
	uploadAsyncResultAPI: 5,
	uploadCompleteV1API:  20,
	accessTokenAPI:       8,
	fileListAPI:          15,
	downloadInfoAPI:      5,
	userInfoAPI:          0,
	moveAPI:              1,
	renameAPI:            0,
	trashAPI:             0,
	preupCreateAPI:       0,
	sliceUploadAPI:       0,
	uploadCompleteAPI:    0,
	uploadURLAPI:         0,
	singleUploadAPI:      0,
	refreshTokenAPI:      10, // 这个是1分钟100，按现在的实现逻辑不是很好处理，先设置为10
}

// var ( //不同情况下获取的AccessTokenQPS限制不同 如下模块化易于拓展
// 	Api = "https://open-api.123pan.com"

// 	AccessToken    = InitApiInfo(Api+"/api/v1/access_token", 1)
// 	RefreshToken   = InitApiInfo(Api+"/api/v1/oauth2/access_token", 1)
// 	UserInfo       = InitApiInfo(Api+"/api/v1/user/info", 1)
// 	FileList       = InitApiInfo(Api+"/api/v2/file/list", 4)
// 	DownloadInfo   = InitApiInfo(Api+"/api/v1/file/download_info", 0)
// 	Mkdir          = InitApiInfo(Api+"/upload/v1/file/mkdir", 2)
// 	Move           = InitApiInfo(Api+"/api/v1/file/move", 1)
// 	Rename         = InitApiInfo(Api+"/api/v1/file/name", 1)
// 	Trash          = InitApiInfo(Api+"/api/v1/file/trash", 2)
// 	UploadCreate   = InitApiInfo(Api+"/upload/v1/file/create", 2)
// 	UploadUrl      = InitApiInfo(Api+"/upload/v1/file/get_upload_url", 0)
// 	UploadComplete = InitApiInfo(Api+"/upload/v1/file/upload_complete", 0)
// 	UploadAsync    = InitApiInfo(Api+"/upload/v1/file/upload_async_result", 1)
// )

func (d *Open123) Request(apiInfo *ApiInfo, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	retryToken := true
	req := base.RestyClient.R()
	req.SetHeaders(map[string]string{

		"platform":     "open_platform",
		"Content-Type": "application/json",
	})

	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}

	log.Debugf("API: %s, QPS: %d, NowLen: %d", apiInfo.url, apiInfo.qps, apiInfo.NowLen())

	apiInfo.Require()
	defer apiInfo.Release()

	// 最多重试2次，共3次
	for range 3 {
		req.SetHeader("authorization", "Bearer "+d.AccessToken)

		res, err := req.Execute(method, apiInfo.url)
		if err != nil {
			return nil, err
		}
		if res.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("status code: %d, body: %s", res.StatusCode(), res.String())
		}
		body := res.Body()

		// 解析为通用响应
		var baseResp BaseResp
		if err = json.Unmarshal(body, &baseResp); err != nil {
			return nil, err
		}

		if baseResp.Code == 0 {
			return body, nil
		} else if baseResp.Code == 401 && retryToken {
			retryToken = false
			if err := d.flushAccessToken(); err != nil {
				return nil, err
			}
		} else if baseResp.Code == 429 {
			time.Sleep(time.Second) // qps是以s为单位，直接延迟1s
			log.Warningf("API: %s, QPS: %d, 请求太频繁，对应API提示过多请减小QPS", apiInfo.url, apiInfo.qps)
		} else {
			return nil, errors.New(baseResp.Message)
		}
	}
	return nil, fmt.Errorf("max retry count exceeded,api : %s", apiInfo.url)

}

func (d *Open123) flushAccessToken() error {
	// 第三方授权应用刷新token
	if d.RefreshToken != "" {
		r := &RefreshTokenResp{}
		rta := d.apiinstance[refreshTokenAPI]
		rta.Require()
		defer rta.Release()

		res, err := base.RestyClient.R().
			SetHeaders(map[string]string{
				"Platform":     "open_platform",
				"Content-Type": "application/json",
			}).
			SetResult(r).
			SetQueryParams(map[string]string{
				"grant_type":    "refresh_token",
				"client_id":     d.ClientID,
				"client_secret": d.ClientSecret,
				"refresh_token": d.RefreshToken,
			}).
			Post(baseURL + refreshTokenAPI)
		if err != nil {
			return err
		}
		if res.StatusCode() != http.StatusOK {
			return fmt.Errorf("refresh token failed: %s,statuscode: %d", res.String(), res.StatusCode())
		}

		if r.Code != 0 {
			return fmt.Errorf("refresh token failed: %s", r.Message)
		}

		d.RefreshToken = r.Data.RefreshToken
		d.AccessToken = r.Data.AccessToken
		return nil
	}
	// 个人开发者获取access token
	at := d.apiinstance[accessTokenAPI]

	at.Require()
	defer at.Release()
	r := &AccessTokenResp{}

	res, err := base.RestyClient.R().
		SetHeaders(map[string]string{
			"Platform":     "open_platform",
			"Content-Type": "application/json",
		}).
		SetResult(r).
		SetQueryParams(map[string]string{
			"client_id":     d.ClientID,
			"client_secret": d.ClientSecret,
		}).
		Post(at.url)
	if err != nil {
		return err
	}
	if res.StatusCode() != http.StatusOK {
		return fmt.Errorf("refresh token failed: %s,statusCode:%d", res.String(), res.StatusCode())
	}

	if r.Code != 0 {
		return fmt.Errorf("refresh token failed: %s", r.Message)
	}
	d.AccessToken = r.Data.AccessToken

	return nil
}

func (d *Open123) getUserInfo() (*UserInfoResp, error) {
	var resp UserInfoResp

	if _, err := d.Request(d.apiinstance[userInfoAPI], http.MethodGet, nil, &resp); err != nil {
		return nil, err
	}

	return &resp, nil
}

func (d *Open123) getFiles(parentFileId int64, limit int, lastFileId int64) (*FileListResp, error) {
	var resp FileListResp

	_, err := d.Request(d.apiinstance[fileListAPI], http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(
			map[string]string{
				"parentFileId": strconv.FormatInt(parentFileId, 10),
				"limit":        strconv.Itoa(limit),
				"lastFileId":   strconv.FormatInt(lastFileId, 10),
				"trashed":      "false",
				"searchMode":   "",
				"searchData":   "",
			})
	}, &resp)

	if err != nil {
		return nil, err
	}

	return &resp, nil
}

func (d *Open123) getDownloadInfo(fileId int64) (*DownloadInfoResp, error) {
	var resp DownloadInfoResp

	_, err := d.Request(d.apiinstance[downloadInfoAPI], http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"fileId": strconv.FormatInt(fileId, 10),
		})
	}, &resp)
	if err != nil {
		return nil, err
	}

	return &resp, nil
}

func (d *Open123) mkdir(parentID int64, name string) error {
	_, err := d.Request(d.apiinstance[mkdirAPI], http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"parentID": strconv.FormatInt(parentID, 10),
			"name":     name,
		})
	}, nil)
	if err != nil {
		return err
	}

	return nil
}

func (d *Open123) move(fileID, toParentFileID int64) error {
	_, err := d.Request(d.apiinstance[moveAPI], http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"fileIDs":        []int64{fileID},
			"toParentFileID": toParentFileID,
		})
	}, nil)
	if err != nil {
		return err
	}

	return nil
}

func (d *Open123) rename(fileId int64, fileName string) error {
	_, err := d.Request(d.apiinstance[renameAPI], http.MethodPut, func(req *resty.Request) {
		req.SetBody(base.Json{
			"fileId":   fileId,
			"fileName": fileName,
		})
	}, nil)
	if err != nil {
		return err
	}

	return nil
}

func (d *Open123) trash(fileId int64) error {
	_, err := d.Request(d.apiinstance[trashAPI], http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"fileIDs": []int64{fileId},
		})
	}, nil)
	if err != nil {
		return err
	}

	return nil
}
