package quark

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Resp struct {
	Status  int    `json:"status"`
	Code    int    `json:"code"`
	Message string `json:"message"`
	// ReqId     string `json:"req_id"`
	// Timestamp int    `json:"timestamp"`
}

var _ model.Obj = (*File)(nil)

type File struct {
	Fid      string `json:"fid"`
	FileName string `json:"file_name"`
	// PdirFid      string `json:"pdir_fid"`
	Category int `json:"category"`
	// FileType     int    `json:"file_type"`
	Size int64 `json:"size"`
	// FormatType   string `json:"format_type"`
	// Status       int    `json:"status"`
	// Tags         string `json:"tags,omitempty"`
	LCreatedAt int64 `json:"l_created_at"`
	LUpdatedAt int64 `json:"l_updated_at"`
	// NameSpace    int    `json:"name_space"`
	// IncludeItems int    `json:"include_items,omitempty"`
	// RiskType     int    `json:"risk_type"`
	// BackupSign   int    `json:"backup_sign"`
	// Duration     int    `json:"duration"`
	// FileSource   string `json:"file_source"`
	File      bool  `json:"file"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
	// PrivateExtra struct {} `json:"_private_extra"`
	// ObjCategory string `json:"obj_category,omitempty"`
	// Thumbnail string `json:"thumbnail,omitempty"`
}

func fileToObj(f File) *model.Object {
	return &model.Object{
		ID:       f.Fid,
		Name:     f.FileName,
		Size:     f.Size,
		Modified: time.UnixMilli(f.UpdatedAt),
		Ctime:    time.UnixMilli(f.CreatedAt),
		IsFolder: !f.File,
	}
}

func (f *File) GetSize() int64 {
	return f.Size
}

func (f *File) GetName() string {
	return f.FileName
}

func (f *File) ModTime() time.Time {
	return time.UnixMilli(f.UpdatedAt)
}

func (f *File) CreateTime() time.Time {
	return time.UnixMilli(f.CreatedAt)
}

func (f *File) IsDir() bool {
	return !f.File
}

func (f *File) GetHash() utils.HashInfo {
	return utils.HashInfo{}
}

func (f *File) GetID() string {
	return f.Fid
}

func (f *File) GetPath() string {
	return ""
}

type SortResp struct {
	Resp
	Data struct {
		List []File `json:"list"`
	} `json:"data"`
	Metadata struct {
		Size  int    `json:"_size"`
		Page  int    `json:"_page"`
		Count int    `json:"_count"`
		Total int    `json:"_total"`
		Way   string `json:"way"`
	} `json:"metadata"`
}

type DownResp struct {
	Resp
	Data []struct {
		// Fid          string `json:"fid"`
		// FileName     string `json:"file_name"`
		// PdirFid      string `json:"pdir_fid"`
		// Category     int    `json:"category"`
		// FileType     int    `json:"file_type"`
		// Size         int    `json:"size"`
		// FormatType   string `json:"format_type"`
		// Status       int    `json:"status"`
		// Tags         string `json:"tags"`
		// LCreatedAt   int64  `json:"l_created_at"`
		// LUpdatedAt   int64  `json:"l_updated_at"`
		// NameSpace    int    `json:"name_space"`
		// Thumbnail    string `json:"thumbnail"`
		DownloadUrl string `json:"download_url"`
		//Md5          string `json:"md5"`
		//RiskType     int    `json:"risk_type"`
		//RangeSize    int    `json:"range_size"`
		//BackupSign   int    `json:"backup_sign"`
		//ObjCategory  string `json:"obj_category"`
		//Duration     int    `json:"duration"`
		//FileSource   string `json:"file_source"`
		//File         bool   `json:"file"`
		//CreatedAt    int64  `json:"created_at"`
		//UpdatedAt    int64  `json:"updated_at"`
		//PrivateExtra struct {
		//} `json:"_private_extra"`
	} `json:"data"`
	//Metadata struct {
	//	Acc2 string `json:"acc2"`
	//	Acc1 string `json:"acc1"`
	//} `json:"metadata"`
}

type TranscodingResp struct {
	Resp
	Data struct {
		DefaultResolution       string `json:"default_resolution"`
		OriginDefaultResolution string `json:"origin_default_resolution"`
		VideoList               []struct {
			Resolution string `json:"resolution"`
			VideoInfo  struct {
				Duration int     `json:"duration"`
				Size     int64   `json:"size"`
				Format   string  `json:"format"`
				Width    int     `json:"width"`
				Height   int     `json:"height"`
				Bitrate  float64 `json:"bitrate"`
				Codec    string  `json:"codec"`
				Fps      float64 `json:"fps"`
				Rotate   int     `json:"rotate"`
				Audio    struct {
					Duration int     `json:"duration"`
					Bitrate  float64 `json:"bitrate"`
					Codec    string  `json:"codec"`
					Channels int     `json:"channels"`
				} `json:"audio"`
				UpdateTime int64  `json:"update_time"`
				URL        string `json:"url"`
				Resolution string `json:"resolution"`
				HlsType    string `json:"hls_type"`
				Finish     bool   `json:"finish"`
				Resoultion string `json:"resoultion"`
				Success    bool   `json:"success"`
			} `json:"video_info,omitempty"`
			// Right          string `json:"right"`
			// MemberRight    string `json:"member_right"`
			// TransStatus    string `json:"trans_status"`
			// Accessable     bool   `json:"accessable"`
			// SupportsFormat string `json:"supports_format"`
			// VideoFuncType  string `json:"video_func_type,omitempty"`
		} `json:"video_list"`
		// AudioList []interface{} `json:"audio_list"`
		FileName  string `json:"file_name"`
		NameSpace int    `json:"name_space"`
		Size      int64  `json:"size"`
		Thumbnail string `json:"thumbnail"`
		//LastPlayInfo struct {
		//	Time int `json:"time"`
		//} `json:"last_play_info"`
		//SeekPreviewData struct {
		//	TotalFrameCount    int `json:"total_frame_count"`
		//	TotalSpriteCount   int `json:"total_sprite_count"`
		//	FrameWidth         int `json:"frame_width"`
		//	FrameHeight        int `json:"frame_height"`
		//	SpriteRow          int `json:"sprite_row"`
		//	SpriteColumn       int `json:"sprite_column"`
		//	PreviewSpriteInfos []struct {
		//		URL        string `json:"url"`
		//		FrameCount int    `json:"frame_count"`
		//		Times      []int  `json:"times"`
		//	} `json:"preview_sprite_infos"`
		//} `json:"seek_preview_data"`
		//ObjKey string `json:"obj_key"`
		//Meta   struct {
		//	Duration int     `json:"duration"`
		//	Size     int64   `json:"size"`
		//	Format   string  `json:"format"`
		//	Width    int     `json:"width"`
		//	Height   int     `json:"height"`
		//	Bitrate  float64 `json:"bitrate"`
		//	Codec    string  `json:"codec"`
		//	Fps      float64 `json:"fps"`
		//	Rotate   int     `json:"rotate"`
		//} `json:"meta"`
		//PreloadLevel       int  `json:"preload_level"`
		//HasSeekPreviewData bool `json:"has_seek_preview_data"`
	} `json:"data"`
}

type UpPreResp struct {
	Resp
	Data struct {
		TaskId    string `json:"task_id"`
		Finish    bool   `json:"finish"`
		UploadId  string `json:"upload_id"`
		ObjKey    string `json:"obj_key"`
		UploadUrl string `json:"upload_url"`
		Fid       string `json:"fid"`
		Bucket    string `json:"bucket"`
		Callback  struct {
			CallbackUrl  string `json:"callbackUrl"`
			CallbackBody string `json:"callbackBody"`
		} `json:"callback"`
		FormatType string `json:"format_type"`
		Size       int    `json:"size"`
		AuthInfo   string `json:"auth_info"`
	} `json:"data"`
	Metadata struct {
		PartThread int    `json:"part_thread"`
		Acc2       string `json:"acc2"`
		Acc1       string `json:"acc1"`
		PartSize   int    `json:"part_size"` // 分片大小
	} `json:"metadata"`
}

type HashResp struct {
	Resp
	Data struct {
		Finish     bool   `json:"finish"`
		Fid        string `json:"fid"`
		Thumbnail  string `json:"thumbnail"`
		FormatType string `json:"format_type"`
	} `json:"data"`
	Metadata struct{} `json:"metadata"`
}

type UpAuthResp struct {
	Resp
	Data struct {
		AuthKey string        `json:"auth_key"`
		Speed   int           `json:"speed"`
		Headers []interface{} `json:"headers"`
	} `json:"data"`
	Metadata struct{} `json:"metadata"`
}

type MemberResp struct {
	Resp
	Data struct {
		MemberType        string `json:"member_type"`
		CreatedAt         uint64 `json:"created_at"`
		SecretUseCapacity int64  `json:"secret_use_capacity"`
		UseCapacity       int64  `json:"use_capacity"`
		IsNewUser         bool   `json:"is_new_user"`
		MemberStatus      struct {
			Vip      string `json:"VIP"`
			ZVip     string `json:"Z_VIP"`
			MiniVip  string `json:"MINI_VIP"`
			SuperVip string `json:"SUPER_VIP"`
		} `json:"member_status"`
		SecretTotalCapacity int64 `json:"secret_total_capacity"`
		TotalCapacity       int64 `json:"total_capacity"`
	} `json:"data"`
	Metadata struct {
		RangeSize     int    `json:"range_size"`
		ServerCurTime uint64 `json:"server_cur_time"`
	} `json:"metadata"`
}
