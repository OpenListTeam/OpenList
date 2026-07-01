package cloudflare_imgbed

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Address          string `json:"address" required:"true" help:"Backend API address of the image hosting service, e.g., https://img.example.com"`
	Token            string `json:"token" required:"true" help:"Authentication Token"`
	SmallChannelName string `json:"smallChannelName" help:"Channel name for regular files (typically <20MB)"`
	LargeChannelName string `json:"largeChannelName" help:"Channel name for large files"`
	LargeChannelType string `json:"largeChannelType" type:"select" options:",huggingface,telegram,cfr2,s3,discord" help:"Large File Channel Type: Hugging Face (Direct Upload)、telegram/cfr2/s3/discord(Multipart Upload)"`
	UploadThread     int    `json:"uploadThread" type:"number" default:"3" help:"Concurrent thread count for HuggingFace chunked direct upload"`
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
