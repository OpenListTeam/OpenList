package protondrive

/*
Package protondrive
Author: Da3zKi7<da3zki7@duck.com>
Date: 2025-09-18

Thanks to @henrybear327 for modded go-proton-api & Proton-API-Bridge

The power of open-source, the force of teamwork and the magic of reverse engineering!


D@' 3z K!7 - The King Of Cracking

Да здравствует Родина))
*/

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/henrybear327/Proton-API-Bridge/common"
)

type Addition struct {
	driver.RootID
	Email              string `json:"email" required:"true" type:"string"`
	Password           string `json:"password" required:"true" type:"string"`
	TwoFACode          string `json:"two_fa_code" type:"string"`
	ChunkSize          int64  `json:"chunk_size" type:"number" default:"100"`
	UseReusableLogin   bool   `json:"use_reusable_login" type:"bool" default:"true" help:"Use reusable login credentials instead of username/password"`
	ReusableCredential common.ReusableCredentialData
}

var config = driver.Config{
	Name:        "ProtonDrive",
	LocalSort:   true,
	OnlyProxy:   true,
	DefaultRoot: "root",
	NoLinkURL:   true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &ProtonDrive{
			Addition: Addition{
				UseReusableLogin: true,
			},
			apiBase:    "https://drive.proton.me/api",
			appVersion: "windows-drive@1.11.3+rclone+proton",
			protonJson: "application/vnd.protonmail.v1+json",
			sdkVersion: "js@0.3.0",
			userAgent:  "ProtonDrive/v1.70.0 (Windows NT 10.0.22000; Win64; x64)",
			webDriveAV: "web-drive@5.2.0+0f69f7a8",
		}
	})
}
