package _139

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	crypto_rand "crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
)

const (
	KEY_HEX_1 = "73634235495062495331515373756c734e7253306c673d3d" // 第一层 AES 解密密钥
	KEY_HEX_2 = "7150714477323633586746674c337538"                 // 第二层 AES 解密密钥
)

// do others that not defined in Driver interface
func (d *Yun139) isFamily() bool {
	return d.Type == "family"
}

func encodeURIComponent(str string) string {
	r := url.QueryEscape(str)
	r = strings.Replace(r, "+", "%20", -1)
	r = strings.Replace(r, "%21", "!", -1)
	r = strings.Replace(r, "%27", "'", -1)
	r = strings.Replace(r, "%28", "(", -1)
	r = strings.Replace(r, "%29", ")", -1)
	r = strings.Replace(r, "%2A", "*", -1)
	return r
}

func calSign(body, ts, randStr string) string {
	body = encodeURIComponent(body)
	strs := strings.Split(body, "")
	sort.Strings(strs)
	body = strings.Join(strs, "")
	body = base64.StdEncoding.EncodeToString([]byte(body))
	res := utils.GetMD5EncodeStr(body) + utils.GetMD5EncodeStr(ts+":"+randStr)
	res = strings.ToUpper(utils.GetMD5EncodeStr(res))
	return res
}

func getTime(t string) time.Time {
	stamp, _ := time.ParseInLocation("20060102150405", t, utils.CNLoc)
	return stamp
}

func (d *Yun139) refreshToken() error {
	if d.ref != nil {
		return d.ref.refreshToken()
	}
	decode, err := base64.StdEncoding.DecodeString(d.Authorization)
	if err != nil {
		return fmt.Errorf("authorization decode failed: %s", err)
	}
	decodeStr := string(decode)
	splits := strings.Split(decodeStr, ":")
	if len(splits) < 3 {
		return fmt.Errorf("authorization is invalid, splits < 3")
	}
	d.Account = splits[1]
	strs := strings.Split(splits[2], "|")
	if len(strs) < 4 {
		return fmt.Errorf("authorization is invalid, strs < 4")
	}
	expiration, err := strconv.ParseInt(strs[3], 10, 64)
	if err != nil {
		return fmt.Errorf("authorization is invalid")
	}
	expiration -= time.Now().UnixMilli()
	if expiration > 1000*60*60*24*15 {
		// Authorization有效期大于15天无需刷新
		return nil
	}
	if expiration < 0 {
		return fmt.Errorf("authorization has expired")
	}

	url := "https://aas.caiyun.feixin.10086.cn:443/tellin/authTokenRefresh.do"
	var resp RefreshTokenResp
	reqBody := "<root><token>" + splits[2] + "</token><account>" + splits[1] + "</account><clienttype>656</clienttype></root>"
	_, err = base.RestyClient.R().
		ForceContentType("application/xml").
		SetBody(reqBody).
		SetResult(&resp).
		Post(url)
	if err != nil || resp.Return != "0" {
		log.Warnf("139yun: failed to refresh token with old token: %v, desc: %s. trying to login with password.", err, resp.Desc)
		newAuth, loginErr := d.loginWithPassword()
		log.Debugf("newAuth: Ok: %s", newAuth)
		if loginErr != nil {
			return fmt.Errorf("failed to login with password after refresh failed: %w", loginErr)
		}
		return nil
	}

	d.Authorization = base64.StdEncoding.EncodeToString([]byte(splits[0] + ":" + splits[1] + ":" + resp.Token))
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Yun139) request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	req := base.RestyClient.R()
	randStr := random.String(16)
	ts := time.Now().Format("2006-01-02 15:04:05")
	if callback != nil {
		callback(req)
	}
	body, err := utils.Json.Marshal(req.Body)
	if err != nil {
		return nil, err
	}
	sign := calSign(string(body), ts, randStr)
	svcType := "1"
	if d.isFamily() {
		svcType = "2"
	}
	req.SetHeaders(map[string]string{
		"Accept":         "application/json, text/plain, */*",
		"CMS-DEVICE":     "default",
		"Authorization":  "Basic " + d.getAuthorization(),
		"mcloud-channel": "1000101",
		"mcloud-client":  "10701",
		//"mcloud-route": "001",
		"mcloud-sign": fmt.Sprintf("%s,%s,%s", ts, randStr, sign),
		//"mcloud-skey":"",
		"mcloud-version":         "7.14.0",
		"Origin":                 "https://yun.139.com",
		"Referer":                "https://yun.139.com/w/",
		"x-DeviceInfo":           "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
		"x-huawei-channelSrc":    "10000034",
		"x-inner-ntwk":           "2",
		"x-m4c-caller":           "PC",
		"x-m4c-src":              "10002",
		"x-SvcType":              svcType,
		"Inner-Hcy-Router-Https": "1",
	})

	var e BaseResp
	req.SetResult(&e)
	log.Debugf("[139] request: %s %s, body: %s", method, url, string(body))
	res, err := req.Execute(method, url)
	if err != nil {
		log.Debugf("[139] request error: %v", err)
		return nil, err
	}
	log.Debugf("[139] response body: %s", res.String())
	if !e.Success {
		// Always try to unmarshal to the specific response type first if 'resp' is provided.
		if resp != nil {
			err = utils.Json.Unmarshal(res.Body(), resp)
			if err != nil {
				log.Debugf("[139] failed to unmarshal response to specific type: %v", err)
				return nil, err // Return unmarshal error
			}
			if createBatchOprTaskResp, ok := resp.(*CreateBatchOprTaskResp); ok {
				log.Debugf("[139] CreateBatchOprTaskResp.Result.ResultCode: %s", createBatchOprTaskResp.Result.ResultCode)
				if createBatchOprTaskResp.Result.ResultCode == "0" {
					goto SUCCESS_PROCESS
				}
			}
		}
		return nil, errors.New(e.Message) // Fallback to original error if not handled
	}
	if resp != nil {
		err = utils.Json.Unmarshal(res.Body(), resp)
		if err != nil {
			return nil, err
		}
	}
SUCCESS_PROCESS:
	return res.Body(), nil
}

func (d *Yun139) requestRoute(data interface{}, resp interface{}) ([]byte, error) {
	url := "https://user-njs.yun.139.com/user/route/qryRoutePolicy"
	req := base.RestyClient.R()
	randStr := random.String(16)
	ts := time.Now().Format("2006-01-02 15:04:05")
	callback := func(req *resty.Request) {
		req.SetBody(data)
	}
	if callback != nil {
		callback(req)
	}
	body, err := utils.Json.Marshal(req.Body)
	if err != nil {
		return nil, err
	}
	sign := calSign(string(body), ts, randStr)
	svcType := "1"
	if d.isFamily() {
		svcType = "2"
	}
	req.SetHeaders(map[string]string{
		"Accept":         "application/json, text/plain, */*",
		"CMS-DEVICE":     "default",
		"Authorization":  "Basic " + d.getAuthorization(),
		"mcloud-channel": "1000101",
		"mcloud-client":  "10701",
		//"mcloud-route": "001",
		"mcloud-sign": fmt.Sprintf("%s,%s,%s", ts, randStr, sign),
		//"mcloud-skey":"",
		"mcloud-version":         "7.14.0",
		"Origin":                 "https://yun.139.com",
		"Referer":                "https://yun.139.com/w/",
		"x-DeviceInfo":           "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
		"x-huawei-channelSrc":    "10000034",
		"x-inner-ntwk":           "2",
		"x-m4c-caller":           "PC",
		"x-m4c-src":              "10002",
		"x-SvcType":              svcType,
		"Inner-Hcy-Router-Https": "1",
	})

	var e BaseResp
	req.SetResult(&e)
	res, err := req.Execute(http.MethodPost, url)
	log.Debugln(res.String())
	if !e.Success {
		return nil, errors.New(e.Message)
	}
	if resp != nil {
		err = utils.Json.Unmarshal(res.Body(), resp)
		if err != nil {
			return nil, err
		}
	}
	return res.Body(), nil
}

func (d *Yun139) post(pathname string, data interface{}, resp interface{}) ([]byte, error) {
	return d.request("https://yun.139.com"+pathname, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, resp)
}

func (d *Yun139) getFiles(catalogID string) ([]model.Obj, error) {
	start := 0
	limit := 100
	files := make([]model.Obj, 0)
	for {
		data := base.Json{
			"catalogID":       catalogID,
			"sortDirection":   1,
			"startNumber":     start + 1,
			"endNumber":       start + limit,
			"filterType":      0,
			"catalogSortType": 0,
			"contentSortType": 0,
			"commonAccountInfo": base.Json{
				"account":     d.getAccount(),
				"accountType": 1,
			},
		}
		var resp GetDiskResp
		_, err := d.post("/orchestration/personalCloud/catalog/v1.0/getDisk", data, &resp)
		if err != nil {
			return nil, err
		}
		for _, catalog := range resp.Data.GetDiskResult.CatalogList {
			f := model.Object{
				ID:       catalog.CatalogID,
				Name:     catalog.CatalogName,
				Size:     0,
				Modified: getTime(catalog.UpdateTime),
				Ctime:    getTime(catalog.CreateTime),
				IsFolder: true,
			}
			files = append(files, &f)
		}
		for _, content := range resp.Data.GetDiskResult.ContentList {
			f := model.ObjThumb{
				Object: model.Object{
					ID:       content.ContentID,
					Name:     content.ContentName,
					Size:     content.ContentSize,
					Modified: getTime(content.UpdateTime),
					HashInfo: utils.NewHashInfo(utils.MD5, content.Digest),
				},
				Thumbnail: model.Thumbnail{Thumbnail: content.ThumbnailURL},
				// Thumbnail: content.BigthumbnailURL,
			}
			files = append(files, &f)
		}
		if start+limit >= resp.Data.GetDiskResult.NodeCount {
			break
		}
		start += limit
	}
	return files, nil
}

func (d *Yun139) newJson(data map[string]interface{}) base.Json {
	common := map[string]interface{}{
		"catalogType": 3,
		"cloudID":     d.CloudID,
		"cloudType":   1,
		"commonAccountInfo": base.Json{
			"account":     d.getAccount(),
			"accountType": 1,
		},
	}
	return utils.MergeMap(data, common)
}

func (d *Yun139) familyGetFiles(catalogID string) ([]model.Obj, error) {
	pageNum := 1
	files := make([]model.Obj, 0)
	for {
		data := d.newJson(base.Json{
			"catalogID":       catalogID,
			"contentSortType": 0,
			"pageInfo": base.Json{
				"pageNum":  pageNum,
				"pageSize": 100,
			},
			"sortDirection": 1,
		})
		var resp QueryContentListResp
		_, err := d.post("/orchestration/familyCloud-rebuild/content/v1.2/queryContentList", data, &resp)
		if err != nil {
			return nil, err
		}
		path := resp.Data.Path
		if catalogID == d.RootFolderID {
			d.RootPath = path
		}
		for _, catalog := range resp.Data.CloudCatalogList {
			f := model.Object{
				ID:       catalog.CatalogID,
				Name:     catalog.CatalogName,
				Size:     0,
				IsFolder: true,
				Modified: getTime(catalog.LastUpdateTime),
				Ctime:    getTime(catalog.CreateTime),
				Path:     path, // 文件夹上一级的Path
			}
			files = append(files, &f)
		}
		for _, content := range resp.Data.CloudContentList {
			f := model.ObjThumb{
				Object: model.Object{
					ID:       content.ContentID,
					Name:     content.ContentName,
					Size:     content.ContentSize,
					Modified: getTime(content.LastUpdateTime),
					Ctime:    getTime(content.CreateTime),
					Path:     path, // 文件所在目录的Path
				},
				Thumbnail: model.Thumbnail{Thumbnail: content.ThumbnailURL},
				// Thumbnail: content.BigthumbnailURL,
			}
			files = append(files, &f)
		}
		if resp.Data.TotalCount == 0 {
			break
		}
		pageNum++
	}
	return files, nil
}

func (d *Yun139) groupGetFiles(catalogID string) ([]model.Obj, error) {
	pageNum := 1
	files := make([]model.Obj, 0)
	for {
		data := d.newJson(base.Json{
			"groupID":         d.CloudID,
			"catalogID":       path.Base(catalogID),
			"contentSortType": 0,
			"sortDirection":   1,
			"startNumber":     pageNum,
			"endNumber":       pageNum + 99,
			"path":            path.Join(d.RootFolderID, catalogID),
		})

		var resp QueryGroupContentListResp
		_, err := d.post("/orchestration/group-rebuild/content/v1.0/queryGroupContentList", data, &resp)
		if err != nil {
			return nil, err
		}
		path := resp.Data.GetGroupContentResult.ParentCatalogID
		if catalogID == d.RootFolderID {
			d.RootPath = path
		}
		for _, catalog := range resp.Data.GetGroupContentResult.CatalogList {
			f := model.Object{
				ID:       catalog.CatalogID,
				Name:     catalog.CatalogName,
				Size:     0,
				IsFolder: true,
				Modified: getTime(catalog.UpdateTime),
				Ctime:    getTime(catalog.CreateTime),
				Path:     catalog.Path, // 文件夹的真实Path， root:/开头
			}
			files = append(files, &f)
		}
		for _, content := range resp.Data.GetGroupContentResult.ContentList {
			f := model.ObjThumb{
				Object: model.Object{
					ID:       content.ContentID,
					Name:     content.ContentName,
					Size:     content.ContentSize,
					Modified: getTime(content.UpdateTime),
					Ctime:    getTime(content.CreateTime),
					Path:     path, // 文件所在目录的Path
				},
				Thumbnail: model.Thumbnail{Thumbnail: content.ThumbnailURL},
				// Thumbnail: content.BigthumbnailURL,
			}
			files = append(files, &f)
		}
		if (pageNum + 99) > resp.Data.GetGroupContentResult.NodeCount {
			break
		}
		pageNum = pageNum + 100
	}
	return files, nil
}

func (d *Yun139) getLink(contentId string) (string, error) {
	data := base.Json{
		"appName":   "",
		"contentID": contentId,
		"commonAccountInfo": base.Json{
			"account":     d.getAccount(),
			"accountType": 1,
		},
	}
	res, err := d.post("/orchestration/personalCloud/uploadAndDownload/v1.0/downloadRequest",
		data, nil)
	if err != nil {
		return "", err
	}
	return jsoniter.Get(res, "data", "downloadURL").ToString(), nil
}

func (d *Yun139) familyGetLink(contentId string, path string) (string, error) {
	data := d.newJson(base.Json{
		"contentID": contentId,
		"path":      path,
	})
	res, err := d.post("/orchestration/familyCloud-rebuild/content/v1.0/getFileDownLoadURL",
		data, nil)
	if err != nil {
		return "", err
	}
	return jsoniter.Get(res, "data", "downloadURL").ToString(), nil
}

func (d *Yun139) groupGetLink(contentId string, path string) (string, error) {
	data := d.newJson(base.Json{
		"contentID": contentId,
		"groupID":   d.CloudID,
		"path":      path,
	})
	res, err := d.post("/orchestration/group-rebuild/groupManage/v1.0/getGroupFileDownLoadURL",
		data, nil)
	if err != nil {
		return "", err
	}
	return jsoniter.Get(res, "data", "downloadURL").ToString(), nil
}

func unicode(str string) string {
	textQuoted := strconv.QuoteToASCII(str)
	textUnquoted := textQuoted[1 : len(textQuoted)-1]
	return textUnquoted
}

func (d *Yun139) personalRequest(pathname string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	url := d.getPersonalCloudHost() + pathname
	req := base.RestyClient.R()
	randStr := random.String(16)
	ts := time.Now().Format("2006-01-02 15:04:05")
	if callback != nil {
		callback(req)
	}
	body, err := utils.Json.Marshal(req.Body)
	if err != nil {
		return nil, err
	}
	sign := calSign(string(body), ts, randStr)
	svcType := "1"
	if d.isFamily() {
		svcType = "2"
	}
	req.SetHeaders(map[string]string{
		"Accept":               "application/json, text/plain, */*",
		"Authorization":        "Basic " + d.getAuthorization(),
		"Caller":               "web",
		"Cms-Device":           "default",
		"Mcloud-Channel":       "1000101",
		"Mcloud-Client":        "10701",
		"Mcloud-Route":         "001",
		"Mcloud-Sign":          fmt.Sprintf("%s,%s,%s", ts, randStr, sign),
		"Mcloud-Version":       "7.14.0",
		"x-DeviceInfo":         "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
		"x-huawei-channelSrc":  "10000034",
		"x-inner-ntwk":         "2",
		"x-m4c-caller":         "PC",
		"x-m4c-src":            "10002",
		"x-SvcType":            svcType,
		"X-Yun-Api-Version":    "v1",
		"X-Yun-App-Channel":    "10000034",
		"X-Yun-Channel-Source": "10000034",
		"X-Yun-Client-Info":    "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||",
		"X-Yun-Module-Type":    "100",
		"X-Yun-Svc-Type":       "1",
	})

	var e BaseResp
	req.SetResult(&e)
	log.Debugf("[139] personal request: %s %s, body: %s", method, url, string(body))
	res, err := req.Execute(method, url)
	if err != nil {
		log.Debugf("[139] personal request error: %v", err)
		return nil, err
	}
	log.Debugf("[139] personal response body: %s", res.String())
	if !e.Success {
		return nil, errors.New(e.Message)
	}
	if resp != nil {
		err = utils.Json.Unmarshal(res.Body(), resp)
		if err != nil {
			return nil, err
		}
	}
	return res.Body(), nil
}

func (d *Yun139) personalPost(pathname string, data interface{}, resp interface{}) ([]byte, error) {
	return d.personalRequest(pathname, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, resp)
}

func (d *Yun139) isboPost(pathname string, data interface{}, resp interface{}) ([]byte, error) {
	url := "https://group.yun.139.com/hcy/mutual/adapter" + pathname
	return d.request(url, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, resp)
}

func getPersonalTime(t string) time.Time {
	stamp, err := time.ParseInLocation("2006-01-02T15:04:05.999-07:00", t, utils.CNLoc)
	if err != nil {
		panic(err)
	}
	return stamp
}

func (d *Yun139) personalGetFiles(fileId string) ([]model.Obj, error) {
	files := make([]model.Obj, 0)
	nextPageCursor := ""
	for {
		data := base.Json{
			"imageThumbnailStyleList": []string{"Small", "Large"},
			"orderBy":                 "updated_at",
			"orderDirection":          "DESC",
			"pageInfo": base.Json{
				"pageCursor": nextPageCursor,
				"pageSize":   100,
			},
			"parentFileId": fileId,
		}
		var resp PersonalListResp
		_, err := d.personalPost("/file/list", data, &resp)
		if err != nil {
			return nil, err
		}
		nextPageCursor = resp.Data.NextPageCursor
		for _, item := range resp.Data.Items {
			isFolder := (item.Type == "folder")
			var f model.Obj
			if isFolder {
				f = &model.Object{
					ID:       item.FileId,
					Name:     item.Name,
					Size:     0,
					Modified: getPersonalTime(item.UpdatedAt),
					Ctime:    getPersonalTime(item.CreatedAt),
					IsFolder: isFolder,
				}
			} else {
				Thumbnails := item.Thumbnails
				var ThumbnailUrl string
				if d.UseLargeThumbnail {
					for _, thumb := range Thumbnails {
						if strings.Contains(thumb.Style, "Large") {
							ThumbnailUrl = thumb.Url
							break
						}
					}
				}
				if ThumbnailUrl == "" && len(Thumbnails) > 0 {
					ThumbnailUrl = Thumbnails[len(Thumbnails)-1].Url
				}
				f = &model.ObjThumb{
					Object: model.Object{
						ID:       item.FileId,
						Name:     item.Name,
						Size:     item.Size,
						Modified: getPersonalTime(item.UpdatedAt),
						Ctime:    getPersonalTime(item.CreatedAt),
						IsFolder: isFolder,
					},
					Thumbnail: model.Thumbnail{Thumbnail: ThumbnailUrl},
				}
			}
			files = append(files, f)
		}
		if len(nextPageCursor) == 0 {
			break
		}
	}
	return files, nil
}

func (d *Yun139) personalGetLink(fileId string) (string, error) {
	data := base.Json{
		"fileId": fileId,
	}
	res, err := d.personalPost("/file/getDownloadUrl",
		data, nil)
	if err != nil {
		return "", err
	}
	cdnUrl := jsoniter.Get(res, "data", "cdnUrl").ToString()
	if cdnUrl != "" {
		return cdnUrl, nil
	} else {
		return jsoniter.Get(res, "data", "url").ToString(), nil
	}
}

func (d *Yun139) getAuthorization() string {
	if d.ref != nil {
		return d.ref.getAuthorization()
	}
	return d.Authorization
}

func (d *Yun139) getAccount() string {
	if d.ref != nil {
		return d.ref.getAccount()
	}
	return d.Account
}

func (d *Yun139) getPersonalCloudHost() string {
	if d.ref != nil {
		return d.ref.getPersonalCloudHost()
	}
	return d.PersonalCloudHost
}

func (d *Yun139) uploadPersonalParts(ctx context.Context, partInfos []PartInfo, uploadPartInfos []PersonalPartInfo, rateLimited *driver.RateLimitReader, p *driver.Progress) error {
	// 确保数组以 PartNumber 从小到大排序
	sort.Slice(uploadPartInfos, func(i, j int) bool {
		return uploadPartInfos[i].PartNumber < uploadPartInfos[j].PartNumber
	})

	for _, uploadPartInfo := range uploadPartInfos {
		index := uploadPartInfo.PartNumber - 1
		if index < 0 || index >= len(partInfos) {
			return fmt.Errorf("invalid PartNumber %d: index out of bounds (partInfos length: %d)", uploadPartInfo.PartNumber, len(partInfos))
		}
		partSize := partInfos[index].PartSize
		log.Debugf("[139] uploading part %+v/%+v", index, len(partInfos))
		limitReader := io.LimitReader(rateLimited, partSize)
		r := io.TeeReader(limitReader, p)
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadPartInfo.UploadUrl, r)
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Content-Length", fmt.Sprint(partSize))
		req.Header.Set("Origin", "https://yun.139.com")
		req.Header.Set("Referer", "https://yun.139.com/")
		req.ContentLength = partSize
		err = func() error {
			res, err := base.HttpClient.Do(req)
			if err != nil {
				return err
			}
			defer res.Body.Close()
			log.Debugf("[139] uploaded: %+v", res)
			if res.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(res.Body)
				return fmt.Errorf("unexpected status code: %d, body: %s", res.StatusCode, string(body))
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Yun139) getPersonalDiskInfo(ctx context.Context) (*PersonalDiskInfoResp, error) {
	data := map[string]interface{}{
		"userDomainId": d.UserDomainID,
	}
	var resp PersonalDiskInfoResp
	_, err := d.request("https://user-njs.yun.139.com/user/disk/getPersonalDiskInfo", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
		req.SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func (d *Yun139) getFamilyDiskInfo(ctx context.Context) (*FamilyDiskInfoResp, error) {
	data := map[string]interface{}{
		"userDomainId": d.UserDomainID,
	}
	var resp FamilyDiskInfoResp
	_, err := d.request("https://user-njs.yun.139.com/user/disk/getFamilyDiskInfo", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
		req.SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

func getMd5(dataStr string) string {
	hash := md5.Sum([]byte(dataStr))
	return fmt.Sprintf("%x", hash)
}

func (d *Yun139) step1_password_login() (string, error) {
	log.Debugf("--- 执行步骤 1: 登录 API ---")
	loginURL := "https://mail.10086.cn/Login/Login.ashx"

	// 密码 SHA1 哈希
	hashedPassword := sha1Hash(fmt.Sprintf("fetion.com.cn:%s", d.Password))
	log.Debugf("DEBUG: 原始密码: %s", d.Password)
	log.Debugf("DEBUG: SHA1 输入: fetion.com.cn:%s", d.Password)
	log.Debugf("DEBUG: 生成的 Password 哈希: %s", hashedPassword)

	cguid := strconv.FormatInt(time.Now().UnixMilli(), 10) // 随机生成 cguid

	loginHeaders := map[string]string{
		"accept":                    "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.7",
		"accept-language":           "zh-CN,zh;q=0.9,zh-TW;q=0.8,en-US;q=0.7,en;q=0.6,en-GB;q=0.5",
		"cache-control":             "max-age=0",
		"content-type":              "application/x-www-form-urlencoded",
		"dnt":                       "1",
		"origin":                    "https://mail.10086.cn",
		"priority":                  "u=0, i",
		"referer":                   fmt.Sprintf("https://mail.10086.cn/default.html?&s=1&v=0&u=%s&m=1&ec=S001&resource=indexLogin&clientid=1003&auto=on&cguid=%s&mtime=45", base64.StdEncoding.EncodeToString([]byte(d.Username)), cguid),
		"sec-ch-ua":                 "\"Microsoft Edge\";v=\"141\", \"Not?A_Brand\";v=\"8\", \"Chromium\";v=\"141\"",
		"sec-ch-ua-mobile":          "?0",
		"sec-ch-ua-platform":        "\"Windows\"",
		"sec-fetch-dest":            "document",
		"sec-fetch-mode":            "navigate",
		"sec-fetch-site":            "same-origin",
		"sec-fetch-user":            "?1",
		"upgrade-insecure-requests": "1",
		"user-agent":                "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0",
		"Cookie":                    d.MailCookies,
	}

	loginData := url.Values{}
	loginData.Set("UserName", d.Username)
	loginData.Set("passOld", "")
	loginData.Set("auto", "on")
	loginData.Set("Password", hashedPassword)
	loginData.Set("webIndexPagePwdLogin", "1")
	loginData.Set("pwdType", "1")
	loginData.Set("clientId", "1003")
	loginData.Set("authType", "2")

	log.Debugf("DEBUG: 登录请求 URL: %s", loginURL)
	log.Debugf("DEBUG: 登录请求 Headers: %+v", loginHeaders)
	log.Debugf("DEBUG: 登录请求 Body: %s", loginData.Encode())

	// 设置客户端不跟随重定向
	client := base.RestyClient.SetRedirectPolicy(resty.NoRedirectPolicy())
	res, err := client.R().
		SetHeaders(loginHeaders).
		SetFormDataFromValues(loginData).
		Post(loginURL)

	if err != nil {
		// 如果是重定向错误，则不作为失败处理，因为我们禁止了自动重定向
		if res != nil && res.StatusCode() >= 300 && res.StatusCode() < 400 {
			log.Debugf("DEBUG: 登录响应 Status Code: %d (Redirect)", res.StatusCode())
		} else {
			return "", fmt.Errorf("step1 login request failed: %w", err)
		}
	} else {
		log.Debugf("DEBUG: 登录响应 Status Code: %d", res.StatusCode())
	}
	// 恢复客户端的默认重定向策略，以免影响后续请求
	base.RestyClient.SetRedirectPolicy(resty.FlexibleRedirectPolicy(10))
	log.Debugf("DEBUG: 登录响应 Headers: %+v", res.Header())

	var sid, extractedCguid string

	// 从 Location 头部提取 sid 和 cguid
	locationHeader := res.Header().Get("Location")
	if locationHeader != "" {
		sidMatch := regexp.MustCompile(`sid=([^&]+)`).FindStringSubmatch(locationHeader)
		cguidMatch := regexp.MustCompile(`cguid=([^&]+)`).FindStringSubmatch(locationHeader)
		if len(sidMatch) > 1 {
			sid = sidMatch[1]
			log.Debugf("DEBUG: 从 Location 提取到 sid: %s", sid)
		}
		if len(cguidMatch) > 1 {
			extractedCguid = cguidMatch[1]
			log.Debugf("DEBUG: 从 Location 提取到 cguid: %s", extractedCguid)
		}
	}

	// 如果 Location 中没有，尝试从 Set-Cookie 中提取
	if sid == "" || extractedCguid == "" {
		setCookieHeaders := res.Header().Values("Set-Cookie")
		for _, cookieStr := range setCookieHeaders {
			ssoSidMatch := regexp.MustCompile(`Os_SSo_Sid=([^;]+)`).FindStringSubmatch(cookieStr)
			cookieCguidMatch := regexp.MustCompile(`cguid=([^;]+)`).FindStringSubmatch(cookieStr)
			if len(ssoSidMatch) > 1 && sid == "" {
				sid = ssoSidMatch[1]
				log.Debugf("DEBUG: 从 Set-Cookie 提取到 sid: %s", sid)
			}
			if len(cookieCguidMatch) > 1 && extractedCguid == "" {
				extractedCguid = cookieCguidMatch[1]
				log.Debugf("DEBUG: 从 Set-Cookie 提取到 cguid: %s", extractedCguid)
			}
		}
	}

	if sid == "" || extractedCguid == "" {
		return "", errors.New("failed to extract sid or cguid from login response")
	}

	// 提取并记录 cookies
	loginUrlObj, _ := url.Parse(loginURL)
	cookies := base.RestyClient.GetClient().Jar.Cookies(loginUrlObj)
	var cookieStrings []string
	for _, cookie := range cookies {
		cookieStrings = append(cookieStrings, cookie.Name+"="+cookie.Value)
	}
	cookieStr := strings.Join(cookieStrings, "; ")
	log.Debugf("DEBUG: 提取到的 Cookies: %s", cookieStr)
	d.MailCookies = cookieStr

	return sid, nil
}

func (d *Yun139) step2_get_single_token(sid string) (string, error) {
	log.Debugf("\n--- 执行步骤 2: 换artifact API ---")
	cguid := strconv.FormatInt(time.Now().UnixMilli(), 10)

	exchangeArtifactURL := fmt.Sprintf("https://smsrebuild1.mail.10086.cn/setting/s?func=%s&sid=%s&cguid=%s", url.QueryEscape("umc:getArtifact"), sid, cguid)

	// 从 MailCookies 中提取 RMKEY
	var rmkey string
	cookies := strings.Split(d.MailCookies, ";")
	for _, cookie := range cookies {
		cookie = strings.TrimSpace(cookie)
		if strings.HasPrefix(cookie, "RMKEY=") {
			rmkey = cookie
			break
		}
	}
	if rmkey == "" {
		return "", errors.New("RMKEY not found in MailCookies")
	}

	exchangePassidHeaders := map[string]string{
		"Host":            "smsrebuild1.mail.10086.cn",
		"Cookie":          rmkey,
		"Content-Type":    "text/xml; charset=utf-8",
		"Accept-Encoding": "gzip",
		"User-Agent":      "okhttp/4.12.0",
	}

	log.Debugf("DEBUG: 换passid 请求 URL: %s", exchangeArtifactURL)
	log.Debugf("DEBUG: 换passid 请求 Headers: %+v", exchangePassidHeaders)

	res, err := base.RestyClient.R().
		SetHeaders(exchangePassidHeaders).
		Post(exchangeArtifactURL)

	if err != nil {
		return "", fmt.Errorf("step2 exchange artifact request failed: %w", err)
	}

	log.Debugf("DEBUG: 换passid 响应 Status Code: %d", res.StatusCode())
	log.Debugf("DEBUG: 换passid 响应 Headers: %+v", res.Header())
	log.Debugf("DEBUG: 换passid 响应 Body: %s...", res.String()[:min(len(res.String()), 500)])

	dycpwd := jsoniter.Get(res.Body(), "var", "artifact").ToString()
	if dycpwd == "" {
		return "", errors.New("failed to extract dycpwd from artifact exchange response")
	}
	log.Debugf("DEBUG: 提取到 dycpwd: %s", dycpwd)

	return dycpwd, nil
}

// --- 辅助函数：加密/解密 ---

// sha1Hash 计算 SHA1 哈希值，返回十六进制字符串。
func sha1Hash(data string) string {
	h := sha1.New()
	h.Write([]byte(data))
	return hex.EncodeToString(h.Sum(nil))
}

// pkcs7_pad PKCS7 填充
func pkcs7_pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(data, padtext...)
}

// pkcs7_unpad PKCS7 去填充
func pkcs7_unpad(data []byte) ([]byte, error) {
	length := len(data)
	if length == 0 {
		return nil, errors.New("pkcs7: data is empty")
	}
	unpadding := int(data[length-1])
	if unpadding > length {
		return nil, errors.New("pkcs7: invalid padding")
	}
	return data[:(length - unpadding)], nil
}

// aes_ecb_decrypt AES/ECB/Pkcs7 解密，输入为十六进制字符串。
func aes_ecb_decrypt(ciphertext []byte, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	if len(ciphertext)%block.BlockSize() != 0 {
		return nil, errors.New("AES ECB decrypt: ciphertext is not a multiple of the block size")
	}

	decrypted := make([]byte, len(ciphertext))
	blockSize := block.BlockSize()

	for bs, be := 0, blockSize; bs < len(ciphertext); bs, be = bs+blockSize, be+blockSize {
		block.Decrypt(decrypted[bs:be], ciphertext[bs:be])
	}

	return pkcs7_unpad(decrypted)
}

// 以下提供 camelCase 的 AES CBC 加解密，供文件中其它位置调用（并支持传入 IV）。
func aesCbcEncrypt(plaintext []byte, key []byte, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("aesCbcEncrypt: iv length %d does not match block size %d", len(iv), block.BlockSize())
	}
	padded := pkcs7_pad(plaintext, block.BlockSize())
	ciphertext := make([]byte, len(padded))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, padded)
	return ciphertext, nil
}

func aesCbcDecrypt(ciphertext []byte, key []byte, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(iv) != block.BlockSize() {
		return nil, fmt.Errorf("aesCbcDecrypt: iv length %d does not match block size %d", len(iv), block.BlockSize())
	}
	if len(ciphertext)%block.BlockSize() != 0 {
		return nil, errors.New("aesCbcDecrypt: ciphertext is not a multiple of the block size")
	}
	decrypted := make([]byte, len(ciphertext))
	mode := cipher.NewCBCDecrypter(block, iv)
	mode.CryptBlocks(decrypted, ciphertext)
	return pkcs7_unpad(decrypted)
}

// sortedJsonStringify 对 JSON 对象进行排序并字符串化。
func sortedJsonStringify(obj interface{}) (string, error) {
	if obj == nil {
		return "null", nil
	}

	switch v := obj.(type) {
	case string:
		// 尝试解析为 JSON，如果成功则递归处理
		var parsed interface{}
		if err := jsoniter.Unmarshal([]byte(v), &parsed); err == nil {
			return sortedJsonStringify(parsed)
		}
		// 如果不是 JSON 字符串，则直接返回 JSON 字符串化的结果
		return jsoniter.MarshalToString(v)
	case int, float64, bool:
		return fmt.Sprintf("%v", v), nil
	case []interface{}:
		var items []string
		for _, item := range v {
			s, err := sortedJsonStringify(item)
			if err != nil {
				return "", err
			}
			items = append(items, s)
		}
		return fmt.Sprintf("[%s]", strings.Join(items, ",")), nil
	case map[string]interface{}:
		sortedKeys := make([]string, 0, len(v))
		for key := range v {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)

		var pairs []string
		for _, key := range sortedKeys {
			value := v[key]
			s, err := sortedJsonStringify(value)
			if err != nil {
				return "", err
			}
			// Use jsoniter.MarshalToString for the key to ensure it's quoted correctly
			keyStr, err := jsoniter.MarshalToString(key)
			if err != nil {
				return "", err
			}
			pairs = append(pairs, fmt.Sprintf("%s:%s", keyStr, s))
		}
		return fmt.Sprintf("{%s}", strings.Join(pairs, ",")), nil
	default:
		// Fallback for other types, e.g., numbers, booleans, or unhandled complex types
		// Use jsoniter's default marshalling for these
		return jsoniter.MarshalToString(v)
	}
}

// yun139EncryptedRequest handles the common encrypted request/response flow.
func (d *Yun139) yun139EncryptedRequest(url string, body interface{}, headers map[string]string, aesKeyHex string, resp interface{}) ([]byte, error) {
	// 1. Decode AES key
	aesKey, err := hex.DecodeString(aesKeyHex)
	if err != nil {
		return nil, fmt.Errorf("yun139EncryptedRequest: failed to decode AES key: %w", err)
	}

	// 2. Marshal and sort the request body
	sortedJson, err := sortedJsonStringify(body)
	if err != nil {
		return nil, fmt.Errorf("yun139EncryptedRequest: failed to marshal and sort body: %w", err)
	}
	log.Debugf("yun139EncryptedRequest: Request Body (plaintext): %s", sortedJson)

	// 3. Encrypt the body using AES/CBC
	iv := make([]byte, 16) // 16 bytes for AES-128
	if _, err := crypto_rand.Read(iv); err != nil {
		return nil, fmt.Errorf("yun139EncryptedRequest: failed to generate IV: %w", err)
	}
	encryptedBody, err := aesCbcEncrypt([]byte(sortedJson), aesKey, iv)
	if err != nil {
		return nil, fmt.Errorf("yun139EncryptedRequest: failed to encrypt body: %w", err)
	}
	payload := base64.StdEncoding.EncodeToString(append(iv, encryptedBody...))

	// 4. Make the request
	res, err := base.RestyClient.R().
		SetHeaders(headers).
		SetBody(payload).
		Post(url)

	if err != nil {
		return nil, fmt.Errorf("yun139EncryptedRequest: http request failed: %w", err)
	}

	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("yun139EncryptedRequest: unexpected status code %d: %s", res.StatusCode(), res.String())
	}

	// 5. Decrypt the response
	respBody := res.Body()
	var decryptedBytes []byte

	if len(respBody) > 0 && respBody[0] == '{' {
		log.Warnf("yun139EncryptedRequest: received a plain JSON response, not an encrypted string. Body: %s", string(respBody))
		decryptedBytes = respBody
	} else {
		decodedResp, err := base64.StdEncoding.DecodeString(string(respBody))
		if err != nil {
			return nil, fmt.Errorf("yun139EncryptedRequest: response base64 decode failed: %w. Body: '%s'", err, string(respBody))
		}

		if len(decodedResp) < 16 {
			return nil, fmt.Errorf("yun139EncryptedRequest: decoded response is too short to be encrypted. Length: %d", len(decodedResp))
		}

		respIv := decodedResp[:16]
		respCiphertext := decodedResp[16:]

		decryptedBytes, err = aesCbcDecrypt(respCiphertext, aesKey, respIv)
		if err != nil {
			return nil, fmt.Errorf("yun139EncryptedRequest: response aes decrypt failed: %w", err)
		}
	}

	log.Debugf("yun139EncryptedRequest: Response Body (decrypted): %s", string(decryptedBytes))

	// 6. Unmarshal to the final response struct
	if resp != nil {
		err = utils.Json.Unmarshal(decryptedBytes, resp)
		if err != nil {
			return nil, fmt.Errorf("yun139EncryptedRequest: failed to unmarshal decrypted response: %w", err)
		}
	}

	return decryptedBytes, nil
}

func (d *Yun139) step3_third_party_login(dycpwd string) (string, error) {
	log.Debugf("\n--- 执行步骤 3: 单点登录 API ---")
	ssoLoginURL := "https://user-njs.yun.139.com/user/thirdlogin"

	// 构建原始请求体
	ssoRequestBodyRaw := base.Json{
		"clientkey_decrypt": "l3TryM&Q+X7@dzwk)qP",
		"clienttype":        "886",
		"cpid":              "507",
		"dycpwd":            dycpwd,
		"extInfo":           base.Json{"ifOpenAccount": "0"},
		"loginMode":         "0",
		"msisdn":            d.Username,
		"pintype":           "13",
		"secinfo":           strings.ToUpper(sha1Hash(fmt.Sprintf("fetion.com.cn:%s", dycpwd))),
		"version":           "20250901",
	}

	ssoLoginHeaders := map[string]string{
		"hcy-cool-flag":       "1",
		"x-huawei-channelSrc": "10246600",
		"x-sdk-channelSrc":    "",
		"x-MM-Source":         "0",
		"x-UserAgent":         "android|23116PN5BC|android15|1.2.6|||1440x3200|10246600",
		"x-DeviceInfo":        "4|127.0.0.1|5|1.2.6|Xiaomi|23116PN5BC||02-00-00-00-00-00|android 15|1440x3200|android|||",
		"Content-Type":        "text/plain;charset=UTF-8",
		"Host":                "user-njs.yun.139.com",
		"Connection":          "Keep-Alive",
		"Accept-Encoding":     "gzip",
		"User-Agent":          "okhttp/3.12.2",
	}

	// 使用通用加密请求函数
	decryptedLayer1StrBytes, err := d.yun139EncryptedRequest(ssoLoginURL, ssoRequestBodyRaw, ssoLoginHeaders, KEY_HEX_1, nil)
	if err != nil {
		return "", fmt.Errorf("step3 encrypted request failed: %w", err)
	}

	hexInner := jsoniter.Get(decryptedLayer1StrBytes, "data").ToString()
	if hexInner == "" {
		return "", errors.New("missing data field in first layer decryption result")
	}
	log.Debugf("DEBUG: 第一层解密提取到 hex_inner: %s...", hexInner[:min(len(hexInner), 50)])

	// 第二层解密
	key2, err := hex.DecodeString(KEY_HEX_2)
	if err != nil {
		return "", fmt.Errorf("failed to decode KEY_HEX_2: %w", err)
	}
	hexInnerBytes, err := hex.DecodeString(hexInner)
	if err != nil {
		return "", fmt.Errorf("failed to decode hex_inner: %w", err)
	}
	finalJsonStrBytes, err := aes_ecb_decrypt(hexInnerBytes, key2)
	if err != nil {
		return "", fmt.Errorf("step3 response layer2 aes ecb decrypt failed: %w", err)
	}
	log.Debugf("DEBUG: 最终解密结果: %s", string(finalJsonStrBytes))

	// 提取 authToken
	authToken := jsoniter.Get(finalJsonStrBytes, "authToken").ToString()
	if authToken == "" {
		return "", errors.New("failed to extract authToken from final decryption result")
	}
	log.Debugf("DEBUG: 提取到 authToken: %s", authToken)

	// 提取 account 和 userDomainId
	account := jsoniter.Get(finalJsonStrBytes, "account").ToString()
	userDomainId := jsoniter.Get(finalJsonStrBytes, "userDomainId").ToString()

	if account == "" || userDomainId == "" {
		return "", errors.New("failed to extract account or userDomainId from final decryption result")
	}

	d.UserDomainID = userDomainId
	newAuthorization := base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("pc:%s:%s", account, authToken)))
	return newAuthorization, nil
}

func (d *Yun139) loginWithPassword() (string, error) {
	if d.Username == "" || d.Password == "" || d.MailCookies == "" {
		return "", errors.New("username, password or mail_cookies is empty")
	}

	passId, err := d.step1_password_login()
	if err != nil {
		return "", err
	}
	log.Infof("Step 1 success, passId: %s", passId)

	token, err := d.step2_get_single_token(passId)
	if err != nil {
		return "", err
	}
	log.Infof("Step 2 success, token: %s", token)

	newAuth, err := d.step3_third_party_login(token)
	if err != nil {
		return "", err
	}
	log.Infof("Step 3 success, new authorization generated.")

	d.Authorization = newAuth // Ensure Authorization is also updated before saving
	op.MustSaveDriverStorage(d)
	return newAuth, nil
}

func (d *Yun139) andAlbumRequest(pathname string, body interface{}, resp interface{}) ([]byte, error) {
	url := "https://group.yun.139.com/hcy/family/adapter/andAlbum/openApi" + pathname

	headers := map[string]string{
		"Host":                "group.yun.139.com",
		"authorization":       "Basic " + d.getAuthorization(),
		"x-svctype":           "2",
		"hcy-cool-flag":       "1",
		"api-version":         "v2",
		"x-huawei-channelsrc": "10246600",
		"x-sdk-channelsrc":    "",
		"x-mm-source":         "0",
		"x-deviceinfo":        "1|127.0.0.1|1|12.3.2|Xiaomi|23116PN5BC||02-00-00-00-00-00|android 15|1440x3200|android|zh||||032|0|", //重要参数
		"content-type":        "application/json; charset=utf-8",
		"user-agent":          "okhttp/4.11.0",
		"accept-encoding":     "gzip",
	}

	return d.yun139EncryptedRequest(url, body, headers, KEY_HEX_1, resp)
}

func (d *Yun139) handleMetaGroupCopy(ctx context.Context, srcObj, dstDir model.Obj) error {
	pathname := "/copyContentCatalog"
	var sourceContentIDs []string
	var sourceCatalogIDs []string
	if srcObj.IsDir() {
		sourceCatalogIDs = append(sourceCatalogIDs, path.Join("root:/", srcObj.GetPath(), srcObj.GetID()))
	} else {
		sourceContentIDs = append(sourceContentIDs, path.Join("root:/", srcObj.GetPath(), srcObj.GetID()))
	}

	destCatalogID := path.Join("root:/", dstDir.GetPath(), dstDir.GetID())
	log.Debugf("[139Yun Group Copy] srcObj ID: %s, srcObj Path: %s, dstDir ID: %s, dstDir Path: %s, destCatalogID: %s", srcObj.GetID(), srcObj.GetPath(), dstDir.GetID(), dstDir.GetPath(), destCatalogID)

	body := base.Json{
		"commonAccountInfo": base.Json{
			"accountType":   "1",
			"accountUserId": d.UserDomainID,
		},
		"destCatalogID":    destCatalogID,
		"destCloudID":      d.CloudID,
		"sourceCatalogIDs": sourceCatalogIDs,
		"sourceCloudID":    d.CloudID,
		"sourceContentIDs": sourceContentIDs,
	}

	var resp base.Json
	_, err := d.andAlbumRequest(pathname, body, &resp)
	return err
}

// getGroupRootByCloudID 查询 group 上层信息，优先返回 parentCatalogID，回退到 catalogList[0].path
func (d *Yun139) getGroupRootByCloudID(cloudID string) (string, error) {
	pathname := "/orchestration/group-rebuild/catalog/v1.0/queryGroupContentList"
	body := base.Json{
		"groupID": cloudID,
		"commonAccountInfo": base.Json{
			"account":     d.getAccount(),
			"accountType": 1,
		},
		"pageInfo": base.Json{
			"pageNum":  1,
			"pageSize": 1,
		},
	}
	var resp base.Json
	_, err := d.post(pathname, body, &resp)
	if err != nil {
		return "", err
	}
	dataObj, _ := resp["data"].(map[string]interface{})
	if dataObj == nil {
		return "", fmt.Errorf("invalid group response data")
	}
	if gcr, ok := dataObj["getGroupContentResult"].(map[string]interface{}); ok {
		if pid, ok := gcr["parentCatalogID"].(string); ok && pid != "" {
			return pid, nil
		}
		if cl, ok := gcr["catalogList"].([]interface{}); ok && len(cl) > 0 {
			if first, ok := cl[0].(map[string]interface{}); ok {
				if p, ok := first["path"].(string); ok && p != "" {
					return p, nil
				}
			}
		}
	}
	return "", fmt.Errorf("no root found in group response")
}

// getFamilyRootPath 查询 family 的上层 path（data.path）
// 返回值已去除前缀 "root:/"（或 "root:"），直接返回纯 ID 或 path 部分，便于持久化为 RootFolderID。
func (d *Yun139) getFamilyRootPath(cloudID string) (string, error) {
	// 使用 v1.2 接口（代码日志中已有该请求），pageSize 取 1 足够获取 path 字段
	pathname := "/orchestration/familyCloud-rebuild/content/v1.2/queryContentList"
	body := base.Json{
		"catalogID":   "",
		"catalogType": 3,
		"cloudID":     cloudID,
		"cloudType":   1,
		"commonAccountInfo": base.Json{
			"account":     d.getAccount(),
			"accountType": 1,
		},
		"contentSortType": 0,
		"pageInfo": base.Json{
			"pageNum":  1,
			"pageSize": 1,
		},
		"sortDirection": 1,
	}
	var resp base.Json
	_, err := d.post(pathname, body, &resp)
	if err != nil {
		return "", err
	}
	dataObj, _ := resp["data"].(map[string]interface{})
	if dataObj == nil {
		return "", fmt.Errorf("invalid family response data")
	}
	// helper to strip "root:/" or "root:" prefix
	stripRoot := func(s string) string {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "root:/")
		s = strings.TrimPrefix(s, "root:")
		return s
	}
	if p, ok := dataObj["path"].(string); ok && p != "" {
		return stripRoot(p), nil
	}
	// 回退：有时 path 在 cloudCatalogList.catalogList 中
	if cl, ok := dataObj["cloudCatalogList"].([]interface{}); ok && len(cl) > 0 {
		if first, ok := cl[0].(map[string]interface{}); ok {
			if p, ok := first["path"].(string); ok && p != "" {
				return stripRoot(p), nil
			}
		}
	}
	return "", fmt.Errorf("no path found in family response")
}
