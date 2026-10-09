package lsky_pro

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Address    string `json:"address" required:"true" help:"Lsky Pro site address, e.g. https://img.example.com"`
	Token      string `json:"token" help:"API token, e.g. 1|xxxx. If empty, email and password are used to request one"`
	Email      string `json:"email" help:"Used to request a token when the token is empty"`
	Password   string `json:"password" help:"Used to request a token when the token is empty"`
	StrategyID string `json:"strategy_id" help:"Storage strategy ID used for uploads, empty for the default strategy"`
}

var config = driver.Config{
	Name:              "LskyPro",
	LocalSort:         true,
	OnlyProxy:         false,
	NoOverwriteUpload: true,
	DefaultRoot:       "0",
}

func init() {
	op.RegisterDriver(func() driver.Driver { return &LskyPro{} })
}
