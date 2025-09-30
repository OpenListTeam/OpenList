package quark_uc_tv

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	driver.RootID
	OrderBy        string `json:"order_by" type:"select" options:"file_name,updated_at" default:"updated_at"`
	OrderDirection string `json:"order_direction" type:"select" options:"asc,desc" default:"desc"`
	// define other
	RefreshToken string `json:"refresh_token" required:"false" default:""`
	// 必要且影响登录,由签名决定
	DeviceID string `json:"device_id"  required:"false" default:""`
	// 登陆所用的数据 无需手动填写
	QueryToken string `json:"query_token" required:"false" default:"" help:"don't edit'"`
	// 视频文件链接获取方式 download(可获取源视频) or streaming(获取转码后的视频)
	VideoLinkMethod string `json:"link_method" required:"true" type:"select" options:"download,streaming" default:"download"`
}

type Conf struct {
	api      string
	clientID string
	signKey  string
	appVer   string
	channel  string
	codeApi  string
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &QuarkUCTV{
			config: driver.Config{
				Name:              "QuarkTV",
				DefaultRoot:       "0",
				NoOverwriteUpload: true,
				NoUpload:          true,
			},
			conf: Conf{
				api:      "https://open-api-drive.quark.cn",
				clientID: "d3194e61504e493eb6222857bccfed94",
				signKey:  "kw2dvtd7p4t3pjl2d9ed9yc8yej8kw2d",
				appVer:   "1.8.2.2",
				channel:  "GENERAL",
				codeApi:  "http://api.extscreen.com/quarkdrive",
			},
		}
	})
	op.RegisterDriver(func() driver.Driver {
		return &QuarkUCTV{
			config: driver.Config{
				Name:              "UCTV",
				DefaultRoot:       "0",
				NoOverwriteUpload: true,
				NoUpload:          true,
			},
			conf: Conf{
				api:      "https://open-api-drive.uc.cn",
				clientID: "5acf882d27b74502b7040b0c65519aa7",
				signKey:  "l3srvtd7p42l0d0x1u8d7yc8ye9kki4d",
				appVer:   "1.7.2.2",
				channel:  "UCTVOFFICIALWEB",
				codeApi:  "http://api.extscreen.com/ucdrive",
			},
		}
	})
}
