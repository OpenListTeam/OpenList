package halalcloudopen

import (
	"context"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/halalcloud/golang-sdk-lite/halalcloud/apiclient"
	sdkUser "github.com/halalcloud/golang-sdk-lite/halalcloud/services/user"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
)

func (d *HalalCloudOpen) Init(ctx context.Context) error {
	if d.uploadThread < 1 || d.uploadThread > 32 {
		d.uploadThread, d.UploadThread = 3, 3
	}
	if d.halalCommon == nil {
		d.halalCommon = &halalCommon{
			UserInfo: &sdkUser.User{},
			refreshTokenFunc: func(token string) error {
				d.Addition.RefreshToken = token
				op.MustSaveDriverStorage(d)
				return nil
			},
		}
	}
	if d.Addition.RefreshToken != "" {
		d.halalCommon.SetRefreshToken(d.Addition.RefreshToken)
	}
	timeout := d.Addition.TimeOut
	if timeout <= 0 {
		timeout = 60
	}
	host := d.Addition.Host
	if host == "" {
		host = "openapi.2dland.cn"
	}

	client := apiclient.NewClient(nil, host, d.Addition.ClientID, d.Addition.ClientSecret, d.halalCommon, apiclient.WithTimeout(time.Second*time.Duration(timeout)))
	d.sdkClient = client
	d.sdkUserFileService = sdkUserFile.NewUserFileService(client)
	d.sdkUserService = sdkUser.NewUserService(client)
	userInfo, err := d.sdkUserService.Get(ctx, &sdkUser.User{})
	if err != nil {
		return err
	}
	d.halalCommon.UserInfo = userInfo
	// 能够获取到用户信息，已经检查了 RefreshToken 的有效性，无需再次检查
	return nil
}
