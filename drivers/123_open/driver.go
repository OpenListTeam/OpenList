package _123_open

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type Open123 struct {
	model.Storage
	Addition
}

func (d *Open123) Config() driver.Config {
	return config
}

func (d *Open123) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Open123) Init(ctx context.Context) error {
	if d.UploadThread < 1 || d.UploadThread > 32 {
		d.UploadThread = 3
	}

	return nil
}

func (d *Open123) Drop(ctx context.Context) error {
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Open123) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	fileLastId := int64(0)
	parentFileId, err := strconv.ParseInt(dir.GetID(), 10, 64)
	if err != nil {
		return nil, err
	}
	res := make([]File, 0)

	for fileLastId != -1 {
		files, err := d.getFiles(parentFileId, 100, fileLastId)
		if err != nil {
			return nil, err
		}
		// 目前123panAPI请求，trashed失效，只能通过遍历过滤
		for i := range files.Data.FileList {
			if files.Data.FileList[i].Trashed == 0 {
				res = append(res, files.Data.FileList[i])
			}
		}
		fileLastId = files.Data.LastFileId
	}
	return utils.SliceConvert(res, func(src File) (model.Obj, error) {
		return src, nil
	})
}

func (d *Open123) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	fileId, _ := strconv.ParseInt(file.GetID(), 10, 64)

	res, err := d.getDownloadInfo(fileId)
	if err != nil {
		return nil, err
	}

	link := model.Link{URL: res.Data.DownloadUrl}
	return &link, nil
}

func (d *Open123) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	parentFileId, _ := strconv.ParseInt(parentDir.GetID(), 10, 64)

	return d.mkdir(parentFileId, dirName)
}

func (d *Open123) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	toParentFileID, _ := strconv.ParseInt(dstDir.GetID(), 10, 64)

	return d.move(srcObj.(File).FileId, toParentFileID)
}

func (d *Open123) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	fileId, _ := strconv.ParseInt(srcObj.GetID(), 10, 64)

	return d.rename(fileId, newName)
}

func (d *Open123) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	// 尝试使用上传+MD5秒传功能实现复制
	// 1. 创建文件
	// parentFileID 父目录id，上传到根目录时填写 0
	parentFileId, err := strconv.ParseInt(dstDir.GetID(), 10, 64)
	if err != nil {
		return fmt.Errorf("parse parentFileID error: %v", err)
	}
	etag := srcObj.(File).Etag
	createResp, err := d.create(parentFileId, srcObj.GetName(), etag, srcObj.GetSize(), 2, false)
	if err != nil {
		return err
	}
	// 是否秒传
	if createResp.Data.Reuse {
		return nil
	}
	return errs.NotSupport
}

func (d *Open123) Remove(ctx context.Context, obj model.Obj) error {
	fileId, _ := strconv.ParseInt(obj.GetID(), 10, 64)

	return d.trash(fileId)
}

func (d *Open123) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// 1. 创建文件
	// parentFileID 父目录id，上传到根目录时填写 0
	parentFileId, err := strconv.ParseInt(dstDir.GetID(), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parse parentFileID error: %v", err)
	}
	// etag 文件md5
	etag := file.GetHash().GetHash(utils.MD5)
	if len(etag) < utils.MD5.Width {
		_, etag, err = stream.CacheFullAndHash(file, &up, utils.MD5)
		if err != nil {
			return nil, err
		}
	}
	createResp, err := d.create(parentFileId, file.GetName(), etag, file.GetSize(), 2, false)
	if err != nil {
		return nil, err
	}
	// 是否秒传
	if createResp.Data.Reuse {
		// 秒传成功才会返回正确的 FileID，否则为 0
		if createResp.Data.FileID != 0 {
			return File{
				FileName: file.GetName(),
				Size:     file.GetSize(),
				FileId:   createResp.Data.FileID,
				Type:     2,
				Etag:     etag,
			}, nil
		}
	}

	// 2. 上传分片
	err = d.Upload(ctx, file, createResp, up)
	if err != nil {
		return nil, err
	}

	// 3. 上传完毕
	for range 60 {
		uploadCompleteResp, err := d.complete(createResp.Data.PreuploadID)
		// 返回错误代码未知，如：20103，文档也没有具体说
		if err == nil && uploadCompleteResp.Data.Completed && uploadCompleteResp.Data.FileID != 0 {
			up(100)
			return File{
				FileName: file.GetName(),
				Size:     file.GetSize(),
				FileId:   uploadCompleteResp.Data.FileID,
				Type:     2,
				Etag:     etag,
			}, nil
		}
		// 若接口返回的completed为 false 时，则需间隔1秒继续轮询此接口，获取上传最终结果。
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("upload complete timeout")
}

var _ driver.Driver = (*Open123)(nil)
var _ driver.PutResult = (*Open123)(nil)
