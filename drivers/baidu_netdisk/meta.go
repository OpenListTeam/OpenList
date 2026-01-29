package baidu_netdisk

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	OrderBy               string `json:"order_by" type:"select" options:"name,time,size" default:"name"`
	OrderDirection        string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	DownloadAPI           string `json:"download_api" type:"select" options:"official,crack,crack_video" default:"official"`
	UseOnlineAPI          bool   `json:"use_online_api" default:"true"`
	APIAddress            string `json:"api_url_address" default:"https://api.oplist.org/baiduyun/renewapi"`
	ClientID              string `json:"client_id"`
	ClientSecret          string `json:"client_secret"`
	CustomCrackUA         string `json:"custom_crack_ua" required:"true" default:"netdisk"`
	AccessToken           string
	RefreshToken          string `json:"refresh_token" required:"true"`
	UploadThread          string `json:"upload_thread" default:"3" help:"1<=thread<=32"`
	UploadSliceTimeout    int    `json:"upload_timeout" type:"number" default:"60" help:"per-slice upload timeout in seconds"`
	UploadAPI             string `json:"upload_api" default:"https://d.pcs.baidu.com"`
	UseDynamicUploadAPI   bool   `json:"use_dynamic_upload_api" default:"true" help:"dynamically get upload api domain, when enabled, the 'Upload API' setting will be used as a fallback if failed to get"`
	CustomUploadPartSize  int64  `json:"custom_upload_part_size" type:"number" default:"0" help:"0 for auto"`
	LowBandwithUploadMode bool   `json:"low_bandwith_upload_mode" default:"false"`
	OnlyListVideoFile     bool   `json:"only_list_video_file" default:"false"`
}

const (
	UPLOAD_FALLBACK_API          = "https://d.pcs.baidu.com" // 备用上传地址
	UPLOAD_URL_EXPIRE_TIME       = time.Minute * 60          // 上传地址有效期(分钟)
	DEFAULT_UPLOAD_SLICE_TIMEOUT = time.Second * 60          // 上传分片请求默认超时时间
	UPLOAD_RETRY_COUNT           = 3
	UPLOAD_RETRY_WAIT_TIME       = time.Second * 1
	UPLOAD_RETRY_MAX_WAIT_TIME   = time.Second * 5
)

var config = driver.Config{
	Name:        "BaiduNetdisk",
	DefaultRoot: "/",
	PreferProxy: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &BaiduNetdisk{}
	})
}
