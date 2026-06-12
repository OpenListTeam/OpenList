package _139

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	//Account       string `json:"account" required:"true"`
	Authorization string `json:"authorization" type:"text" help:"Authorization can be used alone. If empty, use mail_cookies alone for fast login, or mail_cookies + username + password for full login fallback."`
	Username      string `json:"username" help:"Required only when using password login fallback with mail_cookies."`
	Password      string `json:"password" secret:"true" help:"Required only when using password login fallback with mail_cookies."`
	MailCookies   string `json:"mail_cookies" type:"text" help:"Cookies from mail.10086.cn. Can be used alone for fast login, or with username and password for full login fallback."`
	driver.RootID
	Type                 string `json:"type" type:"select" options:"personal_new,family,group,personal" default:"personal_new"`
	CloudID              string `json:"cloud_id"`
	UserDomainID         string `json:"user_domain_id" help:"ud_id in Cookie, fill in to show disk usage"`
	CustomUploadPartSize int64  `json:"custom_upload_part_size" type:"number" default:"0" help:"0 for auto"`
	ReportRealSize       bool   `json:"report_real_size" type:"bool" default:"true" help:"Enable to report the real file size during upload"`
	UseLargeThumbnail    bool   `json:"use_large_thumbnail" type:"bool" default:"false" help:"Enable to use large thumbnail for images"`
}

var config = driver.Config{
	Name:             "139Yun",
	LocalSort:        true,
	ProxyRangeOption: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		d := &Yun139{}
		d.ProxyRange = true
		return d
	})
}
