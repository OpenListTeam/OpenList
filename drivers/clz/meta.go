package clz

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Token string `json:"token" type:"text" required:"true" help:"API Token"`
}

var config = driver.Config{
	Name:        "CLZ",
	LocalSort:   false,
	OnlyProxy:   true, // 因为视频需要流式解密，必须经过中转
	NoCache:     false,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &CLZ{}
	})
}