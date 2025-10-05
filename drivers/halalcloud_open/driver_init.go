package halalcloudopen

import (
	"context"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/halalcloud/golang-sdk-lite/halalcloud/apiclient"
	sdkUser "github.com/halalcloud/golang-sdk-lite/halalcloud/services/user"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
)

func (d *HalalCloudOpen) Init(ctx context.Context) error {
	d.uploadThread, _ = strconv.Atoi(d.UploadThread)
	if d.uploadThread < 1 || d.uploadThread > 32 {
		d.uploadThread, d.UploadThread = 3, "3"
	}
	if d.HalalCommon == nil {
		d.HalalCommon = &HalalCommon{
			Common:   &Common{},
			UserInfo: &sdkUser.User{},
			refreshTokenFunc: func(token string) error {
				d.Addition.RefreshToken = token
				op.MustSaveDriverStorage(d)
				return nil
			},
		}
	}
	if d.Addition.RefreshToken != "" {
		d.HalalCommon.SetRefreshToken(d.Addition.RefreshToken)
	}
	timout := d.Addition.TimeOut
	if timout <= 0 {
		timout = 60
	}
	host := d.Addition.Host
	if host == "" {
		host = "openapi.2dland.cn"
	}

	client := apiclient.NewClient(nil, host, d.Addition.ClientID, d.Addition.ClientSecret, d.HalalCommon)
	d.sdkClient = client
	d.sdkUserFileService = sdkUserFile.NewUserFileService(client)
	d.sdkUserService = sdkUser.NewUserService(client)
	userInfo, err := d.sdkUserService.Get(ctx, &sdkUser.User{})
	if err != nil {
		return err
	}
	d.HalalCommon.UserInfo = userInfo
	// 防止重复登录
	// 检查是否有效
	return nil
}
