package cloudflare_imgbed

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address          string `json:"address" type:"text" required:"true" help:"图床后端 API 地址，例如 https://img.example.com"`
	Token            string `json:"token" type:"text" required:"true" help:"身份认证 Token"`
	SmallChannelName string `json:"smallChannelName" type:"text" help:"普通文件(通常<20MB)上传使用的渠道名称"`
	LargeChannelName string `json:"largeChannelName" type:"text" help:"大文件上传使用的渠道名称"`
	LargeChannelType string `json:"largeChannelType" type:"select" options:",huggingface" help:"大文件渠道的特殊类型（如需直传 HuggingFace，请选 huggingface）"`
	UploadThread     int    `json:"uploadThread" type:"number" default:"3" help:"HuggingFace 分片直传时的并发线程数"`
}

var config = driver.Config{
	Name:        "cloudflare_imgbed",
	LocalSort:   true,
	NoUpload:    false,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &CFImgBed{} })
}