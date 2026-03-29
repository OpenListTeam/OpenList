package _189_tv

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/go-resty/resty/v2"
)

type Cloud189TV struct {
	model.Storage
	Addition
	client        *resty.Client
	tokenInfo     *AppSessionResp
	uploadThread  int
	storageConfig driver.Config

	TempUuid string
	cron     *cron.Cron // 新增 cron 字段
}

func (y *Cloud189TV) Config() driver.Config {
	if y.storageConfig.Name == "" {
		y.storageConfig = config
	}
	return y.storageConfig
}

func (y *Cloud189TV) GetAddition() driver.Additional {
	return &y.Addition
}

func (y *Cloud189TV) Init(ctx context.Context) (err error) {
	// 兼容旧上传接口
	y.storageConfig.NoOverwriteUpload = y.isFamily() && y.Addition.RapidUpload

	// 处理个人云和家庭云参数
	if y.isFamily() && y.RootFolderID == "-11" {
		y.RootFolderID = ""
	}
	if !y.isFamily() && y.RootFolderID == "" {
		y.RootFolderID = "-11"
	}

	// 限制上传线程数
	y.uploadThread, _ = strconv.Atoi(y.UploadThread)
	if y.uploadThread < 1 || y.uploadThread > 32 {
		y.uploadThread, y.UploadThread = 3, "3"
	}

	// 初始化请求客户端
	if y.client == nil {
		y.client = base.NewRestyClient().SetHeaders(
			map[string]string{
				"Accept":     "application/json;charset=UTF-8",
				"User-Agent": "EcloudTV/6.5.5 (PJX110; unknown; home02) Android/35",
			},
		)
	}

	// 避免重复登陆
	if !y.isLogin() || y.Addition.AccessToken == "" {
		if err = y.login(); err != nil {
			return err
		}
	}

	// 处理家庭云ID
	if y.FamilyID == "" {
		if y.FamilyID, err = y.getFamilyID(); err != nil {
			return err
		}
	}

	y.cron = cron.NewCron(time.Minute * 5)
	y.cron.Do(y.keepAlive)

	return err
}

func (y *Cloud189TV) Drop(ctx context.Context) error {
	if y.cron != nil {
		y.cron.Stop()
		y.cron = nil
	}
	return nil
}

func (y *Cloud189TV) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return y.getFiles(ctx, dir.GetID(), y.isFamily())
}

func (y *Cloud189TV) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var downloadUrl struct {
		URL string `json:"fileDownloadUrl"`
	}

	isFamily := y.isFamily()
	fullUrl := ApiUrl
	if isFamily {
		fullUrl += "/family/file"
	}
	fullUrl += "/getFileDownloadUrl.action"

	_, err := y.get(fullUrl, func(r *resty.Request) {
		r.SetContext(ctx)
		r.SetQueryParam("fileId", file.GetID())
		if isFamily {
			r.SetQueryParams(map[string]string{
				"familyId": y.FamilyID,
			})
		} else {
			r.SetQueryParams(map[string]string{
				"dt":   "3",
				"flag": "1",
			})
		}
	}, &downloadUrl, isFamily)
	if err != nil {
		return nil, err
	}

	// 重定向获取真实链接
	downloadUrl.URL = strings.Replace(strings.ReplaceAll(downloadUrl.URL, "&amp;", "&"), "http://", "https://", 1)
	res, err := base.NoRedirectClient.R().SetContext(ctx).SetDoNotParseResponse(true).Get(downloadUrl.URL)
	if err != nil {
		return nil, err
	}
	defer res.RawBody().Close()
	if res.StatusCode() == 302 {
		downloadUrl.URL = res.Header().Get("location")
	}

	like := &model.Link{
		URL: downloadUrl.URL,
		Header: http.Header{
			"User-Agent": []string{base.UserAgent},
		},
	}

	return like, nil
}

func (y *Cloud189TV) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	isFamily := y.isFamily()
	fullUrl := ApiUrl
	if isFamily {
		fullUrl += "/family/file"
	}
	fullUrl += "/createFolder.action"

	var newFolder Cloud189Folder
	_, err := y.post(fullUrl, func(req *resty.Request) {
		req.SetContext(ctx)
		req.SetQueryParams(map[string]string{
			"folderName":   dirName,
			"relativePath": "",
		})
		if isFamily {
			req.SetQueryParams(map[string]string{
				"familyId": y.FamilyID,
				"parentId": parentDir.GetID(),
			})
		} else {
			req.SetQueryParams(map[string]string{
				"parentFolderId": parentDir.GetID(),
			})
		}
	}, &newFolder, isFamily)
	if err != nil {
		return nil, err
	}
	return &newFolder, nil
}

func (y *Cloud189TV) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	isFamily := y.isFamily()
	other := map[string]string{"targetFileName": dstDir.GetName()}

	resp, err := y.CreateBatchTask("MOVE", IF(isFamily, y.FamilyID, ""), dstDir.GetID(), other, BatchTaskInfo{
		FileId:   srcObj.GetID(),
		FileName: srcObj.GetName(),
		IsFolder: BoolToNumber(srcObj.IsDir()),
	})
	if err != nil {
		return nil, err
	}
	if err = y.WaitBatchTask("MOVE", resp.TaskID, time.Millisecond*400); err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (y *Cloud189TV) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	isFamily := y.isFamily()
	queryParam := make(map[string]string)
	fullUrl := ApiUrl
	method := http.MethodPost
	if isFamily {
		fullUrl += "/family/file"
		method = http.MethodGet
		queryParam["familyId"] = y.FamilyID
	}

	var newObj model.Obj
	switch f := srcObj.(type) {
	case *Cloud189File:
		fullUrl += "/renameFile.action"
		queryParam["fileId"] = srcObj.GetID()
		queryParam["destFileName"] = newName
		newObj = &Cloud189File{Icon: f.Icon} // 复用预览
	case *Cloud189Folder:
		fullUrl += "/renameFolder.action"
		queryParam["folderId"] = srcObj.GetID()
		queryParam["destFolderName"] = newName
		newObj = &Cloud189Folder{}
	default:
		return nil, errs.NotSupport
	}

	_, err := y.request(fullUrl, method, func(req *resty.Request) {
		req.SetContext(ctx).SetQueryParams(queryParam)
	}, nil, newObj, isFamily)
	if err != nil {
		return nil, err
	}
	return newObj, nil
}

func (y *Cloud189TV) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	isFamily := y.isFamily()
	other := map[string]string{"targetFileName": dstDir.GetName()}

	resp, err := y.CreateBatchTask("COPY", IF(isFamily, y.FamilyID, ""), dstDir.GetID(), other, BatchTaskInfo{
		FileId:   srcObj.GetID(),
		FileName: srcObj.GetName(),
		IsFolder: BoolToNumber(srcObj.IsDir()),
	})
	if err != nil {
		return err
	}
	return y.WaitBatchTask("COPY", resp.TaskID, time.Second)
}

func (y *Cloud189TV) Remove(ctx context.Context, obj model.Obj) error {
	isFamily := y.isFamily()

	resp, err := y.CreateBatchTask("DELETE", IF(isFamily, y.FamilyID, ""), "", nil, BatchTaskInfo{
		FileId:   obj.GetID(),
		FileName: obj.GetName(),
		IsFolder: BoolToNumber(obj.IsDir()),
	})
	if err != nil {
		return err
	}
	// 批量任务数量限制，过快会导致无法删除
	return y.WaitBatchTask("DELETE", resp.TaskID, time.Millisecond*200)
}

func (y *Cloud189TV) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (newObj model.Obj, err error) {
	overwrite := true
	isFamily := y.isFamily()

	// 响应时间长,按需启用
	if y.Addition.RapidUpload && !stream.IsForceStreamUpload() {
		if newObj, err := y.RapidUpload(ctx, dstDir, stream, isFamily, overwrite); err == nil {
			return newObj, nil
		}
	}

	return y.OldUpload(ctx, dstDir, stream, up, isFamily, overwrite)
}

func (y *Cloud189TV) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	capacityInfo, err := y.getCapacityInfo(ctx)
	if err != nil {
		return nil, err
	}
	var total, used int64
	if y.isFamily() {
		total = capacityInfo.FamilyCapacityInfo.TotalSize
		used = capacityInfo.FamilyCapacityInfo.UsedSize
	} else {
		total = capacityInfo.CloudCapacityInfo.TotalSize
		used = capacityInfo.CloudCapacityInfo.UsedSize
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}
