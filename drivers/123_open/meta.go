package _123_open

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	//  refresh_token方式的AccessToken  【对个人开发者暂未开放】
	RefreshToken string `json:"RefreshToken" required:"false"`

	//  通过 https://www.123pan.com/developer 申请
	ClientID     string `json:"ClientID" required:"false"`
	ClientSecret string `json:"ClientSecret" required:"false"`

	//  直接写入AccessToken, AccessToken有过期时间，不建议直接填写
	AccessToken string `json:"AccessToken" required:"false"`

	//  用户名+密码方式登录的AccessToken可以兼容
	//Username string `json:"username" required:"false"`
	//Password string `json:"password" required:"false"`

	//  上传线程数
	UploadThread int `json:"UploadThread" type:"number" default:"3" help:"the threads of upload"`

	//  使用直链
	DirectLink              bool   `json:"DirectLink" type:"bool" default:"false" required:"false" help:"use direct link when download file"`
	DirectLinkPrivateKey    string `json:"DirectLinkPrivateKey" required:"false" help:"private key for direct link, if URL authentication is enabled"`
	DirectLinkValidDuration int64  `json:"DirectLinkValidDuration" type:"number" default:"30" required:"false" help:"minutes, if URL authentication is enabled"`

	driver.RootID
}

var config = driver.Config{
	Name:        "123 Open",
	DefaultRoot: "0",
	LocalSort:   true,
	PreferProxy: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Open123{}
	})
}
