package terabox

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type Addition struct {
	driver.RootPath
	Cookie string `json:"cookie" required:"true"`
	//JsToken        string `json:"js_token" type:"string" required:"true"`
	DownloadAPI    string `json:"download_api" type:"select" options:"official,crack" default:"official"`
	OrderBy        string `json:"order_by" type:"select" options:"name,time,size" default:"name"`
	OrderDirection string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
}

var config = driver.Config{
	Name:        "Terabox",
	DefaultRoot: "/",
}

func New() driver.Driver {
	return &Terabox{}
}
