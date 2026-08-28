package ilanzou

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/foxxorcat/mopan-sdk-go"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type ILanZou struct {
	model.Storage
	Addition

	userID     string
	account    string
	apiClient  *resty.Client
	linkClient *resty.Client
	upClient   *resty.Client
	conf       Conf
	config     driver.Config
}

func (d *ILanZou) Config() driver.Config {
	return d.config
}

func (d *ILanZou) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *ILanZou) Init(ctx context.Context) error {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return err
	}
	// Keep state isolated per storage. The console and CDN issue cookies that
	// must survive a retry but must never leak to another configured account.
	d.apiClient = base.NewRestyClient().SetCookieJar(jar)
	d.linkClient = base.NewRestyClient().SetCookieJar(jar).SetRedirectPolicy(
		resty.RedirectPolicyFunc(func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		}),
	)
	d.upClient = base.NewRestyClient().SetTimeout(time.Minute * 10)
	if d.UUID == "" {
		res, err := d.unproved("/getUuid", http.MethodGet, nil)
		if err != nil {
			return err
		}
		d.UUID = utils.Json.Get(res, "uuid").ToString()
	}
	res, err := d.proved("/user/account/map", http.MethodGet, nil)
	if err != nil {
		return err
	}
	d.userID = utils.Json.Get(res, "map", "userId").ToString()
	d.account = utils.Json.Get(res, "map", "account").ToString()
	log.Debugf("[ilanzou] init response: %s", res)
	return nil
}

func (d *ILanZou) Drop(ctx context.Context) error {
	return nil
}

func (d *ILanZou) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	offset := 1
	var res []ListItem
	for {
		var resp ListResp
		_, err := d.proved("/record/file/list", http.MethodGet, func(req *resty.Request) {
			params := []string{
				"offset=" + strconv.Itoa(offset),
				"limit=60",
				"folderId=" + dir.GetID(),
				"type=0",
			}
			req.SetQueryString(strings.Join(params, "&")).SetResult(&resp)
		})
		if err != nil {
			return nil, err
		}
		res = append(res, resp.List...)
		if resp.Offset < resp.TotalPage {
			offset++
		} else {
			break
		}
	}
	return utils.SliceConvert(res, func(f ListItem) (model.Obj, error) {
		updTime, err := time.ParseInLocation("2006-01-02 15:04:05", f.UpdTime, time.Local)
		if err != nil {
			return nil, err
		}
		obj := model.Object{
			ID:       strconv.FormatInt(f.FileId, 10),
			Name:     f.FileName,
			Size:     f.FileSize * 1024,
			Modified: updTime,
			Ctime:    updTime,
			IsFolder: false,
		}
		if f.FileType == 2 {
			obj.IsFolder = true
			obj.Size = 0
			obj.ID = strconv.FormatInt(f.FolderId, 10)
			obj.Name = f.FolderName
		}
		return &obj, nil
	})
}

func (d *ILanZou) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	u, err := url.Parse(d.conf.base + "/" + d.conf.unproved + "/file/redirect")
	if err != nil {
		return nil, err
	}
	ts, tsStr, err := getTimestamp(d.conf.secret)
	if err != nil {
		return nil, err
	}

	params := []string{
		"uuid=" + url.QueryEscape(d.UUID),
		"devType=6",
		"devCode=" + url.QueryEscape(d.UUID),
		"devModel=chrome",
		"devVersion=" + url.QueryEscape(d.conf.devVersion),
		"appVersion=",
		"timestamp=" + tsStr,
		"appToken=" + appTokenQueryValue(d.Token),
		"enable=1",
	}
	downloadID, err := mopan.AesEncrypt([]byte(fmt.Sprintf("%s|%s", file.GetID(), d.userID)), d.conf.secret)
	if err != nil {
		return nil, err
	}
	params = append(params, "downloadId="+url.QueryEscape(hex.EncodeToString(downloadID)))
	auth, err := mopan.AesEncrypt([]byte(fmt.Sprintf("%s|%d", file.GetID(), ts)), d.conf.secret)
	if err != nil {
		return nil, err
	}
	params = append(params, "auth="+url.QueryEscape(hex.EncodeToString(auth)))
	u.RawQuery = strings.Join(params, "&")
	realURL := u.String()

	req := d.linkClient.R().SetContext(ctx)
	req.SetHeaders(map[string]string{
		"Origin":          d.conf.site,
		"Referer":         d.conf.site + "/",
		"Accept-Encoding": "gzip",
		"Accept-Language": "zh-CN,zh;q=0.9,en-US,en;q=0.8",
	})
	if d.Addition.Ip != "" {
		req.SetHeader("X-Forwarded-For", d.Addition.Ip)
	}
	res, err := req.Get(realURL)
	if err != nil {
		return nil, err
	}
	location := res.Header().Get("location")
	if location != "" && utils.SliceContains([]int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect}, res.StatusCode()) {
		realURL = location
	} else if res.StatusCode() == http.StatusOK && location != "" {
		realURL = location
	} else if res.StatusCode() == http.StatusOK {
		// Some file types return a 200 JSON resolver response instead of a 3xx.
		realURL = utils.Json.Get(res.Body(), "url").ToString()
		if realURL == "" {
			realURL = utils.Json.Get(res.Body(), "data", "url").ToString()
		}
		if realURL == "" {
			return nil, fmt.Errorf("download resolver returned no URL: %s", utils.Json.Get(res.Body(), "msg").ToString())
		}
	} else {
		return nil, fmt.Errorf("redirect failed, status: %d, location: %s, msg: %s", res.StatusCode(), location, utils.Json.Get(res.Body(), "msg").ToString())
	}
	link := &model.Link{URL: realURL}
	// Probe the CDN for the actual object size; API metadata can differ from
	// the bytes served by the final URL. The timeout bounds Link latency.
	headCtx, cancel := context.WithTimeout(ctx, linkHeadTimeout)
	defer cancel()
	if response, err := d.apiClient.R().SetContext(headCtx).Head(realURL); err == nil && response.StatusCode() >= http.StatusOK && response.StatusCode() < http.StatusMultipleChoices {
		if size, parseErr := strconv.ParseInt(response.Header().Get("Content-Length"), 10, 64); parseErr == nil && size > 0 {
			link.ContentLength = size
		}
	}
	return link, nil
}

func (d *ILanZou) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	res, err := d.proved("/file/folder/save", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{"folderDesc": "", "folderId": parentDir.GetID(), "folderName": dirName})
	})
	if err != nil {
		return nil, err
	}
	return &model.Object{ID: utils.Json.Get(res, "list", 0, "id").ToString(), Name: dirName, Modified: time.Now(), Ctime: time.Now(), IsFolder: true}, nil
}

func (d *ILanZou) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	var fileIDs, folderIDs []string
	if srcObj.IsDir() {
		folderIDs = []string{srcObj.GetID()}
	} else {
		fileIDs = []string{srcObj.GetID()}
	}
	_, err := d.proved("/file/folder/move", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{"folderIds": strings.Join(folderIDs, ","), "fileIds": strings.Join(fileIDs, ","), "targetId": dstDir.GetID()})
	})
	if err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (d *ILanZou) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	var err error
	if srcObj.IsDir() {
		_, err = d.proved("/file/folder/edit", http.MethodPost, func(req *resty.Request) {
			req.SetBody(base.Json{"folderDesc": "", "folderId": srcObj.GetID(), "folderName": newName})
		})
	} else {
		_, err = d.proved("/file/edit", http.MethodPost, func(req *resty.Request) {
			req.SetBody(base.Json{"fileDesc": "", "fileId": srcObj.GetID(), "fileName": newName})
		})
	}
	if err != nil {
		return nil, err
	}
	return &model.Object{ID: srcObj.GetID(), Name: newName, Size: srcObj.GetSize(), Modified: time.Now(), Ctime: srcObj.CreateTime(), IsFolder: srcObj.IsDir()}, nil
}

// iLanzou has no server-side copy primitive. Returning NotImplement delegates
// to OpenList's persistent CopyTaskManager instead of blocking the HTTP request.
func (d *ILanZou) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *ILanZou) Remove(ctx context.Context, obj model.Obj) error {
	var fileIDs, folderIDs []string
	if obj.IsDir() {
		folderIDs = []string{obj.GetID()}
	} else {
		fileIDs = []string{obj.GetID()}
	}
	_, err := d.proved("/file/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{"folderIds": strings.Join(folderIDs, ","), "fileIds": strings.Join(fileIDs, ","), "status": 0})
	})
	return err
}

const (
	DefaultPartSize = 1024 * 1024 * 8

	// The results endpoint is eventually consistent after the upload commit.
	maxUploadCommitRetries = 10
	uploadCommitRetryDelay = time.Second

	linkHeadTimeout = 5 * time.Second
)

func (d *ILanZou) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	etag := s.GetHash().GetHash(utils.MD5)
	var err error
	if len(etag) != utils.MD5.Width {
		_, etag, err = stream.CacheFullAndHash(s, &up, utils.MD5)
		if err != nil {
			return nil, err
		}
	}
	fileSizeKiB := (s.GetSize() + 1023) / 1024
	if fileSizeKiB < 1 {
		fileSizeKiB = 1
	}
	res, err := d.proved("/7n/getUpToken", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{"fileId": "", "fileName": s.GetName(), "fileSize": fileSizeKiB, "folderId": dstDir.GetID(), "md5": etag, "type": 1})
	})
	if err != nil {
		return nil, err
	}
	upToken := utils.Json.Get(res, "upToken").ToString()
	if upToken == "-1" {
		var resp UploadTokenRapidResp
		if err := utils.Json.Unmarshal(res, &resp); err != nil {
			return nil, err
		}
		return &model.Object{ID: strconv.FormatInt(resp.Map.FileID, 10), Name: resp.Map.FileName, Size: s.GetSize(), Modified: s.ModTime(), Ctime: s.CreateTime(), IsFolder: false, HashInfo: utils.NewHashInfo(utils.MD5, etag)}, nil
	}
	now := time.Now()
	// Match the current console's generated Qiniu object key.
	key := fmt.Sprintf("disk/%04d/%02d/%02d/%s/%d.rar", now.Year(), now.Month(), now.Day(), d.account, now.UnixMilli())
	reader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{Reader: &driver.SimpleReaderWithSize{Reader: s, Size: s.GetSize()}, UpdateProgress: up})
	var token string
	if s.GetSize() <= DefaultPartSize {
		res, err := d.upClient.R().SetContext(ctx).SetMultipartFormData(map[string]string{"token": upToken, "key": key, "fname": s.GetName()}).SetMultipartField("file", s.GetName(), s.GetMimetype(), reader).Post("https://upload.qiniup.com/")
		if err != nil {
			return nil, err
		}
		token = utils.Json.Get(res.Body(), "token").ToString()
	} else {
		keyBase64 := base64.URLEncoding.EncodeToString([]byte(key))
		res, err := d.upClient.R().SetHeader("Authorization", "UpToken "+upToken).Post(fmt.Sprintf("https://upload.qiniup.com/buckets/%s/objects/%s/uploads", d.conf.bucket, keyBase64))
		if err != nil {
			return nil, err
		}
		uploadID := utils.Json.Get(res.Body(), "uploadId").ToString()
		parts := make([]Part, 0)
		partNum := (s.GetSize() + DefaultPartSize - 1) / DefaultPartSize
		for i := 1; i <= int(partNum); i++ {
			u := fmt.Sprintf("https://upload.qiniup.com/buckets/%s/objects/%s/uploads/%s/%d", d.conf.bucket, keyBase64, uploadID, i)
			res, err = d.upClient.R().SetContext(ctx).SetHeader("Authorization", "UpToken "+upToken).SetBody(io.LimitReader(reader, DefaultPartSize)).Put(u)
			if err != nil {
				return nil, err
			}
			parts = append(parts, Part{PartNumber: i, ETag: utils.Json.Get(res.Body(), "etag").ToString()})
		}
		res, err = d.upClient.R().SetHeader("Authorization", "UpToken "+upToken).SetBody(base.Json{"fnmae": s.GetName(), "parts": parts}).Post(fmt.Sprintf("https://upload.qiniup.com/buckets/%s/objects/%s/uploads/%s", d.conf.bucket, keyBase64, uploadID))
		if err != nil {
			return nil, err
		}
		token = utils.Json.Get(res.Body(), "token").ToString()
	}
	var resp UploadResultResp
	for i := 0; i < maxUploadCommitRetries; i++ {
		_, err = d.unproved("/7n/results", http.MethodPost, func(req *resty.Request) {
			req.SetQueryString("tokenList=" + token + "&tokenTime=" + time.Now().Format("Mon Jan 02 2006 15:04:05 GMT-0700 (MST)")).SetResult(&resp)
		})
		if err != nil {
			return nil, err
		}
		if len(resp.List) == 0 {
			return nil, fmt.Errorf("upload failed, empty response")
		}
		if resp.List[0].Status == 1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(uploadCommitRetryDelay):
		}
	}
	file := resp.List[0]
	if file.Status != 1 {
		return nil, fmt.Errorf("upload failed, status: %d", file.Status)
	}
	return &model.Object{ID: strconv.FormatInt(file.FileId, 10), Name: file.FileName, Size: s.GetSize(), Modified: s.ModTime(), Ctime: s.CreateTime(), IsFolder: false, HashInfo: utils.NewHashInfo(utils.MD5, etag)}, nil
}

func (d *ILanZou) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	res, err := d.proved("/user/account/map", http.MethodGet, func(req *resty.Request) {
		req.SetContext(ctx)
	})
	if err != nil {
		return nil, err
	}
	vipSize := utils.Json.Get(res, "map", "vipSize").ToInt64() * 1024
	totalSize := utils.Json.Get(res, "map", "totalSize").ToInt64() * 1024
	rewardSize := utils.Json.Get(res, "map", "rewardSize").ToInt64() * 1024
	used := utils.Json.Get(res, "map", "usedSize").ToInt64() * 1024
	return &model.StorageDetails{DiskUsage: model.DiskUsage{TotalSpace: totalSize + rewardSize + vipSize, UsedSpace: used}}, nil
}

var _ driver.Driver = (*ILanZou)(nil)
