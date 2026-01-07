package mega

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	// Usually one of two
	//driver.RootPath
	//driver.RootID
	Email       string `json:"email" required:"true"`
	Password    string `json:"password" required:"true"`
	TwoFACode   string `json:"two_fa_code" required:"false" help:"2FA 6-digit code, filling in the 2FA code alone will not support reloading driver"`
	TwoFASecret string `json:"two_fa_secret" required:"false" help:"2FA secret"`
	MoveToTrash bool   `json:"move_to_trash" default:"true" help:"move to trash when deleting files"`
}

var config = driver.Config{
	Name:      "Mega_nz",
	LocalSort: true,
	OnlyProxy: true,
	NoLinkURL: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Mega{}
	})
}
