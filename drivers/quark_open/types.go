package quark_open

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Resp struct {
	CommonRsp
	Errno     int    `json:"errno"`
	ErrorInfo string `json:"error_info"`
}

type CommonRsp struct {
	Status int    `json:"status"`
	ReqID  string `json:"req_id"`
}

type UserInfo struct {
	UserID    string `json:"user_id"`
	Nickname  string `json:"nickname"`
	AvatarURL string `json:"avatar_url"`
}

type UserInfoResp struct {
	CommonRsp
	Data UserInfo `json:"data"`
}

type RefreshTokenOnlineAPIResp struct {
	RefreshToken string `json:"refresh_token"`
	AccessToken  string `json:"access_token"`
	AppID        string `json:"app_id"`
	SignKey      string `json:"sign_key"`
	ErrorMessage string `json:"text"`
}

type File struct {
	Fid          string `json:"fid"`
	ParentFid    string `json:"parent_fid"`
	Category     int64  `json:"category"`
	FileName     string `json:"filename"`
	Size         int64  `json:"size"`
	FileType     string `json:"file_type"`
	ThumbnailURL string `json:"thumbnail_url"`
	ContentHash  string `json:"content_hash"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

func fileToObj(f File) *model.ObjThumb {
	return &model.ObjThumb{
		Object: model.Object{
			ID:       f.Fid,
			Name:     f.FileName,
			Size:     f.Size,
			Modified: time.UnixMilli(f.UpdatedAt),
			IsFolder: f.FileType == "0",
			Ctime:    time.UnixMilli(f.CreatedAt),
		},
		Thumbnail: model.Thumbnail{Thumbnail: f.ThumbnailURL},
	}
}

type QueryCursor struct {
	Version string `json:"version"`
	Token   string `json:"token"`
}

type FileListResp struct {
	CommonRsp
	Data struct {
		FileList        []File      `json:"file_list"`
		LastPage        bool        `json:"last_page"`
		NextQueryCursor QueryCursor `json:"next_query_cursor"`
	} `json:"data"`
}

type FileLikeResp struct {
	CommonRsp
	Data struct {
		Fid         string `json:"fid"`
		Size        int    `json:"size"`
		FileName    string `json:"file_name"`
		DownloadURL string `json:"download_url"`
	} `json:"data"`
}

type UpPreResp struct {
	CommonRsp
	Data struct {
		Finish        bool   `json:"finish"`
		TaskID        string `json:"task_id"`
		Fid           string `json:"fid"`
		CommonHeaders struct {
			XOssContentSha256 string `json:"X-Oss-Content-Sha256"`
			XOssDate          string `json:"X-Oss-Date"`
		} `json:"common_headers"`
		UploadUrls []struct {
			PartNumber    int `json:"part_number"`
			SignatureInfo struct {
				AuthType  string `json:"auth_type"`
				Signature string `json:"signature"`
			} `json:"signature_info"`
			UploadURL string `json:"upload_url"`
			Expired   int64  `json:"expired"`
		} `json:"upload_urls"`
		PartSize int64 `json:"part_size"`
	} `json:"data"`
}

type UpUrlInfo struct {
	UploadUrls []struct {
		PartNumber    int `json:"part_number"`
		PartSize      int `json:"part_size"`
		SignatureInfo struct {
			AuthType  string `json:"auth_type"`
			Signature string `json:"signature"`
		} `json:"signature_info"`
		UploadURL string `json:"upload_url"`
	} `json:"upload_urls"`
	CommonHeaders struct {
		XOssContentSha256 string `json:"X-Oss-Content-Sha256"`
		XOssDate          string `json:"X-Oss-Date"`
	} `json:"common_headers"`
	UploadID string `json:"upload_id"`
}

type UpUrlResp struct {
	CommonRsp
	Data UpUrlInfo `json:"data"`
}

type UpFinishResp struct {
	CommonRsp
	Data struct {
		TaskID     string `json:"task_id"`
		Fid        string `json:"fid"`
		Finish     bool   `json:"finish"`
		PdirFid    string `json:"pdir_fid"`
		Thumbnail  string `json:"thumbnail"`
		FormatType string `json:"format_type"`
		Size       int    `json:"size"`
	} `json:"data"`
}
