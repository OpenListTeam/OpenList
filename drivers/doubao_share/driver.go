package doubao_share

import (
	"context"
	"errors"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type DoubaoShare struct {
	model.Storage
	Addition
	RootFiles []RootFileList
}

func (d *DoubaoShare) Config() driver.Config {
	return config
}

func (d *DoubaoShare) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *DoubaoShare) Init(ctx context.Context) error {
	// 初始化 虚拟分享列表
	if err := d.initShareList(); err != nil {
		return err
	}

	return nil
}

func (d *DoubaoShare) Drop(ctx context.Context) error {
	return nil
}

// 潜在bug：配置二级目录时，可能会出问题
func (d *DoubaoShare) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	// 检查是否为根目录
	if dir.GetID() == "" && dir.GetPath() == "/" {
		return d.listRootDirectory(ctx)
	}

	// 非根目录，处理不同情况
	if fo, ok := dir.(*FileObject); ok {
		if fo.ShareID == "" {
			// 虚拟目录，需要列出子目录
			return d.listVirtualDirectoryContent(dir)
		} else {
			// 具有分享ID的目录，获取此分享下的文件
			shareId, relativePath, err := d._findShareAndPath(dir)
			if err != nil {
				return nil, err
			}
			return d.getFilesInPath(ctx, shareId, dir.GetID(), relativePath)
		}
	}

	// 使用通用方法
	shareId, relativePath, err := d._findShareAndPath(dir)
	if err != nil {
		return nil, err
	}

	// 获取指定路径下的文件
	return d.getFilesInPath(ctx, shareId, dir.GetID(), relativePath)
}

func (d *DoubaoShare) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var downloadUrl string

	if u, ok := file.(*FileObject); ok {
		switch u.NodeType {
		case VideoType, AudioType:
			var r GetVideoFileUrlResp
			_, err := d.request("/samantha/media/get_play_info", http.MethodPost, func(req *resty.Request) {
				req.SetBody(base.Json{
					"key":      u.Key,
					"share_id": u.ShareID,
					"node_id":  file.GetID(),
				})
			}, &r)
			if err != nil {
				return nil, err
			}

			downloadUrl = r.Data.OriginalMediaInfo.MainURL
		default:
			var r GetDownloadInfoResp
			_, err := d.request("/samantha/aispace/get_download_info", http.MethodPost, func(req *resty.Request) {
				req.SetBody(base.Json{
					"requests": []base.Json{{"node_id": file.GetID()}},
				})
			}, &r)
			if err != nil {
				return nil, err
			}

			downloadUrl = r.Data.DownloadInfos[0].MainURL
		}

		// 生成标准的Content-Disposition
		contentDisposition := utils.GenerateContentDisposition(u.Name)

		return &model.Link{
			URL: downloadUrl,
			Header: http.Header{
				"User-Agent":          []string{UserAgent},
				"Content-Disposition": []string{contentDisposition},
			},
		}, nil
	}

	return nil, errors.New("can't convert obj to URL")
}

func (d *DoubaoShare) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	// TODO get archive file meta-info, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *DoubaoShare) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	// TODO list args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *DoubaoShare) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	// TODO return link of file args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

//func (d *DoubaoShare) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*DoubaoShare)(nil)
