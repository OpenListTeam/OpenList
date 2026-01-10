package _123

import (
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type File struct {
	FileName    string    `json:"FileName"`
	Size        int64     `json:"Size"`
	UpdateAt    time.Time `json:"UpdateAt"`
	FileId      int64     `json:"FileId"`
	Type        int       `json:"Type"`
	Etag        string    `json:"Etag"`
	S3KeyFlag   string    `json:"S3KeyFlag"`
	DownloadUrl string    `json:"DownloadUrl"`
}

func (f File) CreateTime() time.Time {
	return f.UpdateAt
}

func (f File) GetHash() utils.HashInfo {
	return utils.NewHashInfo(utils.MD5, f.Etag)
}

func (f File) GetPath() string {
	return ""
}

func (f File) GetSize() int64 {
	return f.Size
}

func (f File) GetName() string {
	return f.FileName
}

func (f File) ModTime() time.Time {
	return f.UpdateAt
}

func (f File) IsDir() bool {
	return f.Type == 1
}

func (f File) GetID() string {
	return strconv.FormatInt(f.FileId, 10)
}

func (f File) Thumb() string {
	if f.DownloadUrl == "" {
		return ""
	}
	du, err := url.Parse(f.DownloadUrl)
	if err != nil {
		return ""
	}
	du.Path = strings.TrimSuffix(du.Path, "_24_24") + "_70_70"
	query := du.Query()
	query.Set("w", "70")
	query.Set("h", "70")
	if !query.Has("type") {
		query.Set("type", strings.TrimPrefix(path.Base(f.FileName), "."))
	}
	if !query.Has("trade_key") {
		query.Set("trade_key", "123pan-thumbnail")
	}
	du.RawQuery = query.Encode()
	return du.String()
}

var _ model.Obj = (*File)(nil)
var _ model.Thumb = (*File)(nil)

//func (f File) Thumb() string {
//
//}
//var _ model.Thumb = (*File)(nil)

type Files struct {
	//BaseResp
	Data struct {
		Next     string `json:"Next"`
		Total    int    `json:"Total"`
		InfoList []File `json:"InfoList"`
	} `json:"data"`
}

//type DownResp struct {
//	//BaseResp
//	Data struct {
//		DownloadUrl string `json:"DownloadUrl"`
//	} `json:"data"`
//}

type UploadResp struct {
	//BaseResp
	Data struct {
		AccessKeyId     string `json:"AccessKeyId"`
		Bucket          string `json:"Bucket"`
		Key             string `json:"Key"`
		SecretAccessKey string `json:"SecretAccessKey"`
		SessionToken    string `json:"SessionToken"`
		FileId          int64  `json:"FileId"`
		Reuse           bool   `json:"Reuse"`
		EndPoint        string `json:"EndPoint"`
		StorageNode     string `json:"StorageNode"`
		UploadId        string `json:"UploadId"`
	} `json:"data"`
}

type S3PreSignedURLs struct {
	Data struct {
		PreSignedUrls map[string]string `json:"presignedUrls"`
	} `json:"data"`
}

type UserInfoResp struct {
	Data struct {
		Uid            int64  `json:"UID"`
		Nickname       string `json:"Nickname"`
		SpaceUsed      int64  `json:"SpaceUsed"`
		SpacePermanent int64  `json:"SpacePermanent"`
		SpaceTemp      int64  `json:"SpaceTemp"`
		FileCount      int    `json:"FileCount"`
	} `json:"data"`
}

type offlineResolveResp struct {
	Data struct {
		List []struct {
			Result  int    `json:"result"`
			ID      int64  `json:"id"`
			ErrCode int    `json:"err_code"`
			ErrMsg  string `json:"err_msg"`
			Files   []struct {
				ID int64 `json:"id"`
			} `json:"files"`
		} `json:"list"`
	} `json:"data"`
}

type offlineSubmitResp struct {
	Data struct {
		TaskList []struct {
			TaskID int64 `json:"task_id"`
			Result int   `json:"result"`
		} `json:"task_list"`
	} `json:"data"`
}

type offlineTaskListResp struct {
	Data struct {
		HasRun bool          `json:"has_run"`
		List   []offlineTask `json:"list"`
		Total  int           `json:"total"`
	} `json:"data"`
}

type offlineTask struct {
	TaskID     int64   `json:"task_id"`
	Name       string  `json:"name"`
	Status     int     `json:"status"`
	Size       int64   `json:"size"`
	ThirdTask  string  `json:"third_task_id"`
	Downloaded int64   `json:"downloaded"`
	Progress   float64 `json:"progress"`
	UploadIDR  int64   `json:"upload_idr"`
	UploadName string  `json:"upload_name"`
	Type       string  `json:"type"`
	Speed      int64   `json:"speed"`
}
