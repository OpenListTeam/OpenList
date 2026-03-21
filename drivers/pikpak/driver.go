package pikpak

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type PikPak struct {
	model.Storage
	Addition
	*Common
	RefreshToken string
	AccessToken  string
	authMu       sync.RWMutex
	persistMu    sync.Mutex
}

func (d *PikPak) Config() driver.Config {
	return config
}

func (d *PikPak) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *PikPak) saveStorage(update func()) {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	if update != nil {
		update()
	}
	op.MustSaveDriverStorage(d)
}

func (d *PikPak) Init(ctx context.Context) (err error) {
	if d.Common == nil {
		d.Common = &Common{
			client:       base.NewRestyClient(),
			CaptchaToken: "",
			UserID:       "",
			DeviceID:     genDeviceID(),
			UserAgent:    "",
		}
	}

	d.ClientID = WebClientID
	d.ClientSecret = WebClientSecret
	d.ClientVersion = WebClientVersion
	d.PackageName = WebPackageName
	d.Algorithms = WebAlgorithms
	d.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:129.0) Gecko/20100101 Firefox/129.0"
	if d.Platform == "web" {
		d.saveStorage(func() {
			d.Platform = ""
		})
	} else if d.Platform != "" {
		return fmt.Errorf("legacy pikpak %q profile was removed; recreate this storage with the current PikPak driver settings", d.Platform)
	}

	if d.Addition.RefreshToken != "" {
		d.setRefreshTokenState(d.Addition.RefreshToken)
	}

	if d.Addition.DeviceID != "" {
		d.SetDeviceID(d.Addition.DeviceID)
	} else {
		if d.GetDeviceID() == "" || len(d.GetDeviceID()) != 32 {
			d.SetDeviceID(genDeviceID())
		}
		d.saveStorage(func() {
			d.Addition.DeviceID = d.GetDeviceID()
		})
	}

	if err = d.ensureAuthorized(); err != nil {
		return err
	}

	return nil
}

func (d *PikPak) Drop(ctx context.Context) error {
	return nil
}

func (d *PikPak) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *PikPak) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var resp File
	var url string
	queryParams := map[string]string{
		"_magic":         "2021",
		"usage":          "FETCH",
		"thumbnail_size": "SIZE_LARGE",
	}
	if !d.DisableMediaLink {
		queryParams["usage"] = "CACHE"
	}
	_, err := d.request(fmt.Sprintf("https://api-drive.mypikpak.net/drive/v1/files/%s", file.GetID()),
		http.MethodGet, func(req *resty.Request) {
			req.SetContext(ctx).
				SetQueryParams(queryParams)
		}, &resp)
	if err != nil {
		return nil, err
	}
	url = resp.WebContentLink

	if !d.DisableMediaLink && len(resp.Medias) > 0 && resp.Medias[0].Link.Url != "" {
		log.Debugln("use media link")
		url = resp.Medias[0].Link.Url
	}

	return &model.Link{
		URL: url,
	}, nil
}

func (d *PikPak) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(base.Json{
			"kind":      "drive#folder",
			"parent_id": parentDir.GetID(),
			"name":      dirName,
		})
	}, nil)
	return err
}

func (d *PikPak) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files:batchMove", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(base.Json{
			"ids": []string{srcObj.GetID()},
			"to": base.Json{
				"parent_id": dstDir.GetID(),
			},
		})
	}, nil)
	return err
}

func (d *PikPak) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files/"+srcObj.GetID(), http.MethodPatch, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(base.Json{
			"name": newName,
		})
	}, nil)
	return err
}

func (d *PikPak) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files:batchCopy", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(base.Json{
			"ids": []string{srcObj.GetID()},
			"to": base.Json{
				"parent_id": dstDir.GetID(),
			},
		})
	}, nil)
	return err
}

func (d *PikPak) Remove(ctx context.Context, obj model.Obj) error {
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files:batchTrash", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(base.Json{
			"ids": []string{obj.GetID()},
		})
	}, nil)
	return err
}

func (d *PikPak) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	sha1Str := stream.GetHash().GetHash(hash_extend.GCID)

	if len(sha1Str) < hash_extend.GCID.Width {
		var err error
		_, sha1Str, err = streamPkg.CacheFullAndHash(stream, &up, hash_extend.GCID, stream.GetSize())
		if err != nil {
			return err
		}
	}

	var resp UploadTaskData
	res, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"kind":        "drive#file",
			"name":        stream.GetName(),
			"size":        stream.GetSize(),
			"hash":        strings.ToUpper(sha1Str),
			"upload_type": "UPLOAD_TYPE_RESUMABLE",
			"objProvider": base.Json{"provider": "UPLOAD_TYPE_UNKNOWN"},
			"parent_id":   dstDir.GetID(),
			"folder_type": "NORMAL",
		})
	}, &resp)
	if err != nil {
		return err
	}

	// 秒传成功
	if resp.Resumable == nil {
		log.Debugln(string(res))
		return nil
	}

	params := resp.Resumable.Params

	if stream.GetSize() <= 10*utils.MB { // 文件大小 小于10MB，改用普通模式上传
		return d.UploadByOSS(ctx, &params, stream, up)
	}
	// 分片上传
	return d.UploadByMultipart(ctx, &params, stream.GetSize(), stream, up)
}

func (d *PikPak) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	var about AboutResponse
	_, err := d.request("https://api-drive.mypikpak.com/drive/v1/about", http.MethodGet, func(req *resty.Request) {
		req.SetContext(ctx)
	}, &about)
	if err != nil {
		return nil, err
	}
	total, err := strconv.ParseInt(about.Quota.Limit, 10, 64)
	if err != nil {
		return nil, err
	}
	used, err := strconv.ParseInt(about.Quota.Usage, 10, 64)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}

// 离线下载文件
func (d *PikPak) OfflineDownload(ctx context.Context, fileUrl string, parentDir model.Obj, fileName string) (*OfflineTask, error) {
	requestBody := base.Json{
		"kind":        "drive#file",
		"name":        fileName,
		"upload_type": "UPLOAD_TYPE_URL",
		"url": base.Json{
			"url": fileUrl,
		},
		"parent_id":   parentDir.GetID(),
		"folder_type": "",
	}

	var resp OfflineDownloadResp
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).
			SetBody(requestBody)
	}, &resp)
	if err != nil {
		return nil, err
	}

	return &resp.Task, err
}

/*
获取离线下载任务列表
phase 可能的取值：
PHASE_TYPE_RUNNING, PHASE_TYPE_ERROR, PHASE_TYPE_COMPLETE, PHASE_TYPE_PENDING
*/
func (d *PikPak) OfflineList(ctx context.Context, nextPageToken string, phase []string) ([]OfflineTask, error) {
	res := make([]OfflineTask, 0)
	url := "https://api-drive.mypikpak.net/drive/v1/tasks"

	if len(phase) == 0 {
		phase = []string{"PHASE_TYPE_RUNNING", "PHASE_TYPE_ERROR", "PHASE_TYPE_COMPLETE", "PHASE_TYPE_PENDING"}
	}
	params := map[string]string{
		"type":           "offline",
		"thumbnail_size": "SIZE_SMALL",
		"limit":          "10000",
		"page_token":     nextPageToken,
		"with":           "reference_resource",
	}

	// 处理 phase 参数
	if len(phase) > 0 {
		filters := base.Json{
			"phase": map[string]string{
				"in": strings.Join(phase, ","),
			},
		}
		filtersJSON, err := json.Marshal(filters)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal filters: %w", err)
		}
		params["filters"] = string(filtersJSON)
	}

	var resp OfflineListResp
	_, err := d.request(url, http.MethodGet, func(req *resty.Request) {
		req.SetContext(ctx).
			SetQueryParams(params)
	}, &resp)
	if err != nil {
		return nil, fmt.Errorf("failed to get offline list: %w", err)
	}
	res = append(res, resp.Tasks...)
	return res, nil
}

func (d *PikPak) DeleteOfflineTasks(ctx context.Context, taskIDs []string, deleteFiles bool) error {
	url := "https://api-drive.mypikpak.net/drive/v1/tasks"
	params := map[string]string{
		"task_ids":     strings.Join(taskIDs, ","),
		"delete_files": strconv.FormatBool(deleteFiles),
	}
	_, err := d.request(url, http.MethodDelete, func(req *resty.Request) {
		req.SetContext(ctx).
			SetQueryParams(params)
	}, nil)
	if err != nil {
		return fmt.Errorf("failed to delete tasks %v: %w", taskIDs, err)
	}
	return nil
}

var _ driver.Driver = (*PikPak)(nil)
