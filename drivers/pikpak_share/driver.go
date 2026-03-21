package pikpak_share

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/op"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type PikPakShare struct {
	model.Storage
	Addition
	*Common
	PassCodeToken string
	passCodeMu    sync.RWMutex
	persistMu     sync.Mutex
}

func (d *PikPakShare) Config() driver.Config {
	return config
}

func (d *PikPakShare) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *PikPakShare) saveStorage(update func()) {
	d.persistMu.Lock()
	defer d.persistMu.Unlock()
	if update != nil {
		update()
	}
	op.MustSaveDriverStorage(d)
}

func (d *PikPakShare) SetPassCodeToken(token string) {
	d.passCodeMu.Lock()
	defer d.passCodeMu.Unlock()
	d.PassCodeToken = token
}

func (d *PikPakShare) GetPassCodeToken() string {
	d.passCodeMu.RLock()
	defer d.passCodeMu.RUnlock()
	return d.PassCodeToken
}

func (d *PikPakShare) Init(ctx context.Context) error {
	if d.Common == nil {
		d.Common = &Common{
			DeviceID:  genDeviceID(),
			UserAgent: "",
		}
	}
	if d.Platform == "web" {
		d.saveStorage(func() {
			d.Platform = ""
		})
	} else if d.Platform != "" {
		return fmt.Errorf("legacy pikpak_share %q profile was removed; recreate this storage with the current PikPakShare driver settings", d.Platform)
	}

	if d.Addition.DeviceID != "" {
		d.SetDeviceID(d.Addition.DeviceID)
	} else {
		if d.GetDeviceID() == "" || len(d.GetDeviceID()) != 32 {
			d.SetDeviceID(genDeviceID())
		}
		d.saveStorage(func() {
			d.Addition.DeviceID = d.GetDeviceID()
		})
	}

	d.ClientID = WebClientID
	d.ClientSecret = WebClientSecret
	d.ClientVersion = WebClientVersion
	d.PackageName = WebPackageName
	d.Algorithms = WebAlgorithms
	d.UserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:129.0) Gecko/20100101 Firefox/129.0"

	// 获取CaptchaToken
	err := d.RefreshCaptchaToken(GetAction(http.MethodGet, "https://api-drive.mypikpak.net/drive/v1/share:batch_file_info"), "")
	if err != nil {
		return err
	}

	if d.SharePwd != "" {
		return d.getSharePassToken()
	}

	return nil
}

func (d *PikPakShare) Drop(ctx context.Context) error {
	return nil
}

func (d *PikPakShare) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *PikPakShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var resp ShareResp
	query := map[string]string{
		"share_id":        d.ShareId,
		"file_id":         file.GetID(),
		"pass_code_token": d.GetPassCodeToken(),
	}
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/share/file_info", http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(query)
	}, &resp)
	if err != nil {
		return nil, err
	}

	downloadUrl := resp.FileInfo.WebContentLink
	if downloadUrl == "" && len(resp.FileInfo.Medias) > 0 {
		// 使用转码后的链接
		if d.Addition.UseTransCodingAddress && len(resp.FileInfo.Medias) > 1 {
			downloadUrl = resp.FileInfo.Medias[1].Link.Url
		} else {
			downloadUrl = resp.FileInfo.Medias[0].Link.Url
		}

	}

	return &model.Link{
		URL: downloadUrl,
	}, nil
}

var _ driver.Driver = (*PikPakShare)(nil)
