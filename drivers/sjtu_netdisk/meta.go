package sjtu_netdisk

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	OrderBy     string `json:"order_by" type:"select" required:"true" options:"name,modificationTime,size" default:"name"`
	OrderByType string `json:"order_by_type" type:"select" required:"true" options:"asc,desc" default:"asc"`
	UserToken   string `json:"user_token" type:"text" required:"true" secret:"true" help:"After signing in to pan.sjtu.edu.cn, refresh the page and locate the GET request https://pan.sjtu.edu.cn/user/v1/user/1/<UserId>?user_token=<UserToken>&with_belonging_teams=false&pf=. Copy the user_token query parameter value."`
	UserId      string `json:"user_id" type:"text" required:"true" help:"In the same GET request, copy the UserId path parameter."`
	KeepAlive   string `json:"keep_alive" type:"text" required:"true" secret:"true" help:"In that request's response headers, find Set-Cookie (for example, keepalive='<KeepAlive>; path=/; HttpOnly') and copy the KeepAlive value."`
}

var config = driver.Config{
	Name:              "SJTUNetdisk",
	LocalSort:         false,
	OnlyProxy:         false,
	NoCache:           false,
	NoUpload:          false,
	NeedMs:            false,
	DefaultRoot:       "/",
	CheckStatus:       false,
	Alert:             "",
	NoOverwriteUpload: false,
}

var apiURL = "https://pan.sjtu.edu.cn/api/v1"

// Used by refreshToken.
var tokenURL = "https://pan.sjtu.edu.cn/user/v1/space/1/personal"

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &SJTUNetdisk{}
	})
}
