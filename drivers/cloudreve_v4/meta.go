package cloudreve_v4

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	// Usually one of two
	driver.RootPath
	// driver.RootID
	// define other
	Address             string `json:"address" required:"true"`
	Username            string `json:"username"`
	Password            string `json:"password"`
	AccessToken         string `json:"access_token"`
	RefreshToken        string `json:"refresh_token"`
	CustomUA            string `json:"custom_ua"`
	EnableFolderSize    bool   `json:"enable_folder_size"`
	EnableThumb         bool   `json:"enable_thumb"`
	EnableVersionUpload bool   `json:"enable_version_upload"`
	HideUploading       bool   `json:"hide_uploading"`
	OrderBy             string `json:"order_by" type:"select" options:"name,size,updated_at,created_at" default:"name" required:"true"`
	OrderDirection      string `json:"order_direction" type:"select" options:"asc,desc" default:"asc" required:"true"`
}

var config = driver.Config{
	Name:              "Cloudreve V4",
	DefaultRoot:       "cloudreve://my",
	CheckStatus:       true,
	NoOverwriteUpload: true,
}

func New() driver.Driver {
	return &CloudreveV4{}
}
