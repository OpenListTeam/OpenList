package ctfile

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// Addition holds all user-configurable fields for the CtFile driver.
type Addition struct {
	// Session is the OpenAPI token generated in the CtFile backend under
	// 「开放接口登录密钥管理」. Required.
	Session string `json:"session" required:"true" secret:"true" help:"OpenAPI session token from 开放接口登录密钥管理"`

	// CloudType selects namespace: "public" (default) or "private".
	CloudType string `json:"cloud_type" type:"select" options:"public,private" default:"public"`

	// BaseURL is the API root, defaulting to https://rest.ctfile.com/v1.
	BaseURL string `json:"base_url" default:"https://rest.ctfile.com/v1" help:"API base URL"`

	// OrderBy controls server-side sort field.
	OrderBy string `json:"order_by" type:"select" options:"name,size,time" default:"name"`

	// OrderDirection is the sort direction.
	OrderDirection string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`

	// UserAgent overrides the default Chrome-like UA sent with every request.
	UserAgent string `json:"user_agent" default:"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"`

	driver.RootID
}

var config = driver.Config{
	Name:        "CtFile",
	DefaultRoot: "0",
	LocalSort:   true,
	NoUpload:    false,
	OnlyProxy:   false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &CtFile{}
	})
}
