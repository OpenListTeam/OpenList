package aliyundrive_share

import (
	"context"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type AliyundriveShare struct {
	model.Storage
	Addition
	AccessToken string
	ShareToken  string
	DriveId     string
	cron        *cron.Cron

	limiter *limiter
}

func (d *AliyundriveShare) Config() driver.Config {
	return config
}

func (d *AliyundriveShare) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *AliyundriveShare) Init(ctx context.Context) error {
	d.limiter = getLimiter()
	err := d.refreshToken(ctx)
	if err != nil {
		d.limiter.free()
		d.limiter = nil
		return err
	}
	err = d.getShareToken(ctx)
	if err != nil {
		d.limiter.free()
		d.limiter = nil
		return err
	}
	d.cron = cron.NewCron(time.Hour * 2)
	d.cron.Do(func() {
		err := d.refreshToken(ctx)
		if err != nil {
			log.Errorf("%+v", err)
		}
	})
	return nil
}

func (d *AliyundriveShare) Drop(ctx context.Context) error {
	if d.cron != nil {
		d.cron.Stop()
	}
	d.limiter.free()
	d.limiter = nil
	d.DriveId = ""
	return nil
}

func (d *AliyundriveShare) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(ctx, dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *AliyundriveShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	data := base.Json{
		"drive_id": d.DriveId,
		"file_id":  file.GetID(),
		// // Only ten minutes lifetime
		"expire_sec": 600,
		"share_id":   d.ShareId,
	}
	var resp ShareLinkResp
	_, err := d.request(ctx, limiterLink, "https://api.alipan.com/v2/file/get_share_link_download_url", http.MethodPost, func(req *resty.Request) {
		req.SetHeader(CanaryHeaderKey, CanaryHeaderValue).SetBody(data).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}
	return &model.Link{
		Header: http.Header{
			"Referer": []string{"https://www.alipan.com/"},
		},
		URL: resp.DownloadUrl,
	}, nil
}

func (d *AliyundriveShare) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	var resp base.Json
	var url string
	data := base.Json{
		"share_id": d.ShareId,
		"file_id":  args.Obj.GetID(),
	}
	switch args.Method {
	case "doc_preview":
		url = "https://api.alipan.com/v2/file/get_office_preview_url"
	case "video_preview":
		url = "https://api.alipan.com/v2/file/get_video_preview_play_info"
		data["category"] = "live_transcoding"
	default:
		return nil, errs.NotSupport
	}
	_, err := d.request(ctx, limiterOther, url, http.MethodPost, func(req *resty.Request) {
		req.SetBody(data).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}
	return resp, nil
}

var _ driver.Driver = (*AliyundriveShare)(nil)
