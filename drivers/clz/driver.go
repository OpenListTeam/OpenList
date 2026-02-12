package clz

import (
	"context"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type CLZ struct {
	model.Storage
	Addition
}

func (d *CLZ) Config() driver.Config           { return config }
func (d *CLZ) GetAddition() driver.Additional { return &d.Addition }
func (d *CLZ) Init(ctx context.Context) error { return nil }

func (d *CLZ) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var res Resp[File]
	data := map[string]interface{}{
		"path":   dir.GetPath(),
		"offset": 0,
		"limit":  100,
	}
	err := d.request("cloud/get_files_h5", data, &res) [cite: 104]
	if err != nil {
		return nil, err
	}

	objs := make([]model.Obj, len(res.Files))
	for i, f := range res.Files {
		objs[i] = f.ToObj()
	}
	return objs, nil
}

func (d *CLZ) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var res VideoResp
	data := map[string]interface{}{
		"fid":          file.GetID(),
		"request_type": "cloud/get_play_link_zhangfei_5325", // 使用新接口 [cite: 179]
	}
	err := d.request("cloud/get_play_link_zhangfei_5325", data, &res)
	if err != nil {
		return nil, err
	}

	link := &model.Link{URL: res.VideoURL}

	// 如果视频已加密，通过驱动进行中转解密 [cite: 144, 176]
	if res.IsEncrypted {
		link.RangeReadCloser = func(ctx context.Context, httpRange model.HttpRange) (io.ReadCloser, error) {
			resp, err := http.Get(res.VideoURL)
			if err != nil {
				return nil, err
			}
			return &DecryptReader{rc: resp.Body}, nil
		}
	}
	return link, nil
}

func (d *CLZ) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	data := map[string]interface{}{
		"path":     parentDir.GetPath(),
		"dir_name": dirName,
	}
	err := d.request("cloud/create_dir", data, nil) [cite: 105]
	return nil, err
}

func (d *CLZ) Remove(ctx context.Context, obj model.Obj) error {
	data := map[string]interface{}{
		"fid": obj.GetID(),
	}
	return d.request("cloud/delete_file", data, nil) [cite: 107]
}

var _ driver.Driver = (*CLZ)(nil)