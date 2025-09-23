package baidu_netdisk

import (
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type TokenErrResp struct {
	ErrorDescription string `json:"error_description"`
	Error            string `json:"error"`
}

type File struct {
	//TkbindId     int    `json:"tkbind_id"`
	//OwnerType    int    `json:"owner_type"`
	Category int `json:"category"`
	//RealCategory string `json:"real_category"`
	FsId int64 `json:"fs_id"`
	//OperId      int   `json:"oper_id"`
	Thumbs struct {
		//Icon string `json:"icon"`
		Url3 string `json:"url3"`
		//Url2 string `json:"url2"`
		//Url1 string `json:"url1"`
	} `json:"thumbs"`
	//Wpfile         int    `json:"wpfile"`

	Size int64 `json:"size"`
	//ExtentTinyint7 int    `json:"extent_tinyint7"`
	Path string `json:"path"`
	//Share          int    `json:"share"`
	//Pl             int    `json:"pl"`
	ServerFilename string `json:"server_filename"`
	Md5            string `json:"md5"`
	//OwnerId        int    `json:"owner_id"`
	//Unlist int `json:"unlist"`
	Isdir int `json:"isdir"`

	// list resp
	ServerCtime int64 `json:"server_ctime"`
	ServerMtime int64 `json:"server_mtime"`
	LocalMtime  int64 `json:"local_mtime"`
	LocalCtime  int64 `json:"local_ctime"`
	//ServerAtime    int64    `json:"server_atime"` `

	// only create and precreate resp
	Ctime int64 `json:"ctime"`
	Mtime int64 `json:"mtime"`
}

func fileToObj(f File) *model.ObjThumb {
	if f.ServerFilename == "" {
		f.ServerFilename = path.Base(f.Path)
	}
	if f.ServerCtime == 0 {
		f.ServerCtime = f.Ctime
	}
	if f.ServerMtime == 0 {
		f.ServerMtime = f.Mtime
	}
	return &model.ObjThumb{
		Object: model.Object{
			ID:       strconv.FormatInt(f.FsId, 10),
			Path:     f.Path,
			Name:     f.ServerFilename,
			Size:     f.Size,
			Modified: time.Unix(f.ServerMtime, 0),
			Ctime:    time.Unix(f.ServerCtime, 0),
			IsFolder: f.Isdir == 1,

			// 直接获取的MD5是错误的
			HashInfo: utils.NewHashInfo(utils.MD5, DecryptMd5(f.Md5)),
		},
		Thumbnail: model.Thumbnail{Thumbnail: f.Thumbs.Url3},
	}
}

type ListResp struct {
	Errno     int    `json:"errno"`
	GuidInfo  string `json:"guid_info"`
	List      []File `json:"list"`
	RequestId int64  `json:"request_id"`
	Guid      int    `json:"guid"`
}

type DownloadResp struct {
	Errmsg string `json:"errmsg"`
	Errno  int    `json:"errno"`
	List   []struct {
		//Category    int    `json:"category"`
		//DateTaken   int    `json:"date_taken,omitempty"`
		Dlink string `json:"dlink"`
		//Filename    string `json:"filename"`
		//FsId        int64  `json:"fs_id"`
		//Height      int    `json:"height,omitempty"`
		//Isdir       int    `json:"isdir"`
		//Md5         string `json:"md5"`
		//OperId      int    `json:"oper_id"`
		//Path        string `json:"path"`
		//ServerCtime int    `json:"server_ctime"`
		//ServerMtime int    `json:"server_mtime"`
		//Size        int    `json:"size"`
		//Thumbs      struct {
		//	Icon string `json:"icon,omitempty"`
		//	Url1 string `json:"url1,omitempty"`
		//	Url2 string `json:"url2,omitempty"`
		//	Url3 string `json:"url3,omitempty"`
		//} `json:"thumbs"`
		//Width int `json:"width,omitempty"`
	} `json:"list"`
	//Names struct {
	//} `json:"names"`
	RequestId string `json:"request_id"`
}

type DownloadResp2 struct {
	Errno int `json:"errno"`
	Info  []struct {
		//ExtentTinyint4 int `json:"extent_tinyint4"`
		//ExtentTinyint1 int `json:"extent_tinyint1"`
		//Bitmap string `json:"bitmap"`
		//Category int `json:"category"`
		//Isdir int `json:"isdir"`
		//Videotag int `json:"videotag"`
		Dlink string `json:"dlink"`
		//OperID int64 `json:"oper_id"`
		//PathMd5 int `json:"path_md5"`
		//Wpfile int `json:"wpfile"`
		//LocalMtime int `json:"local_mtime"`
		/*Thumbs struct {
			Icon string `json:"icon"`
			URL3 string `json:"url3"`
			URL2 string `json:"url2"`
			URL1 string `json:"url1"`
		} `json:"thumbs"`*/
		//PlaySource int `json:"play_source"`
		//Share int `json:"share"`
		//FileKey string `json:"file_key"`
		//Errno int `json:"errno"`
		//LocalCtime int `json:"local_ctime"`
		//Rotate int `json:"rotate"`
		//Metadata time.Time `json:"metadata"`
		//Height int `json:"height"`
		//SampleRate int `json:"sample_rate"`
		//Width int `json:"width"`
		//OwnerType int `json:"owner_type"`
		//Privacy int `json:"privacy"`
		//ExtentInt3 int64 `json:"extent_int3"`
		//RealCategory string `json:"real_category"`
		//SrcLocation string `json:"src_location"`
		//MetaInfo string `json:"meta_info"`
		//ID string `json:"id"`
		//Duration int `json:"duration"`
		//FileSize string `json:"file_size"`
		//Channels int `json:"channels"`
		//UseSegment int `json:"use_segment"`
		//ServerCtime int `json:"server_ctime"`
		//Resolution string `json:"resolution"`
		//OwnerID int `json:"owner_id"`
		//ExtraInfo string `json:"extra_info"`
		//Size int `json:"size"`
		//FsID int64 `json:"fs_id"`
		//ExtentTinyint3 int `json:"extent_tinyint3"`
		//Md5 string `json:"md5"`
		//Path string `json:"path"`
		//FrameRate int `json:"frame_rate"`
		//ExtentTinyint2 int `json:"extent_tinyint2"`
		//ServerFilename string `json:"server_filename"`
		//ServerMtime int `json:"server_mtime"`
		//TkbindID int `json:"tkbind_id"`
	} `json:"info"`
	RequestID int64 `json:"request_id"`
}

type PrecreateResp struct {
	Errno      int   `json:"errno"`
	RequestId  int64 `json:"request_id"`
	ReturnType int   `json:"return_type"`

	// return_type=1
	Path      string `json:"path"`
	Uploadid  string `json:"uploadid"`
	BlockList []int  `json:"block_list"`

	// return_type=2
	File File `json:"info"`
}

// PrecreateReq  预上传请求
type PrecreateReq struct {
	Path       string   `json:"path"`                  // 上传后使用的文件绝对路径（需urlencode）
	Size       int64    `json:"size"`                  // 文件或目录大小，单位B
	Isdir      int      `json:"isdir"`                 // 是否为目录，0 文件，1 目录
	BlockList  []string `json:"block_list"`            // 文件各分片MD5数组的json串
	Autoinit   int      `json:"autoinit"`              // 固定值1
	Rtype      int      `json:"rtype,omitempty"`       // 文件命名策略，非必填
	Uploadid   string   `json:"uploadid,omitempty"`    // 上传ID，非必填
	ContentMd5 string   `json:"content-md5,omitempty"` // 文件MD5，非必填
	SliceMd5   string   `json:"slice-md5,omitempty"`   // 文件校验段的MD5，非必填
	LocalCtime string   `json:"local_ctime,omitempty"` // 客户端创建时间，非必填
	LocalMtime string   `json:"local_mtime,omitempty"` // 客户端修改时间，非必填
}

// SliceupCompleteReq  分片上传完成请求
type SliceUpCompleteReq struct {
	Path       string   `json:"path"`                  // 上传后使用的文件绝对路径（需urlencode），与预上传precreate接口中的path保持一致
	Size       int64    `json:"size"`                  // 文件或目录的大小，必须与实际大小一致
	Isdir      int      `json:"isdir"`                 // 是否目录，0 文件、1 目录，与预上传precreate接口中的isdir保持一致
	BlockList  []string `json:"block_list"`            // 文件各分片md5数组的json串，与预上传precreate接口中的block_list保持一致
	Uploadid   string   `json:"uploadid"`              // 预上传precreate接口下发的uploadid
	Rtype      int      `json:"rtype,omitempty"`       // 文件命名策略，默认0
	LocalCtime int64    `json:"local_ctime,omitempty"` // 客户端创建时间(精确到秒)，默认为当前时间戳
	LocalMtime int64    `json:"local_mtime,omitempty"` // 客户端修改时间(精确到秒)，默认为当前时间戳
	ZipQuality int      `json:"zip_quality,omitempty"` // 图片压缩程度，有效值50、70、100（带此参数时，zip_sign 参数需要一并带上）
	ZipSign    string   `json:"zip_sign,omitempty"`    // 未压缩原始图片文件真实md5（带此参数时，zip_quality 参数需要一并带上）
	IsRevision int      `json:"is_revision,omitempty"` // 是否需要多版本支持，1为支持，0为不支持，默认为0
	Mode       int      `json:"mode,omitempty"`        // 上传方式，1手动、2批量上传、3文件自动备份、4相册自动备份、5视频自动备份
	ExifInfo   string   `json:"exif_info,omitempty"`   // exif信息，json字符串，orientation、width、height、recovery为必传字段
}

// SliceUpCompleteResp  分片上传完成响应
type SliceUpCompleteResp struct {
	Errno          int    `json:"errno"`           // 错误码
	FsID           uint64 `json:"fs_id"`           // 文件在云端的唯一标识ID
	Md5            string `json:"md5,omitempty"`   // 文件的MD5，只有提交文件时才返回，提交目录时没有该值
	ServerFilename string `json:"server_filename"` // 文件名
	Category       int    `json:"category"`        // 分类类型, 1 视频 2 音频 3 图片 4 文档 5 应用 6 其他 7 种子
	Path           string `json:"path"`            // 上传后使用的文件绝对路径
	Size           uint64 `json:"size"`            // 文件大小，单位B
	Ctime          uint64 `json:"ctime"`           // 文件创建时间
	Mtime          uint64 `json:"mtime"`           // 文件修改时间
	Isdir          int    `json:"isdir"`           // 是否目录，0 文件、1 目录
type QuotaResp struct {
	Errno     int    `json:"errno"`
	RequestId int64  `json:"request_id"`
	Total     uint64 `json:"total"`
	Used      uint64 `json:"used"`
	//Free      uint64 `json:"free"`
	//Expire    bool   `json:"expire"`
}
