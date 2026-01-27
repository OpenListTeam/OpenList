package teldrive

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

type Teldrive struct {
	model.Storage
	Addition
}

func (d *Teldrive) Config() driver.Config {
	return config
}

func (d *Teldrive) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Teldrive) Init(ctx context.Context) error {
	d.Address = strings.TrimSuffix(d.Address, "/")
	if d.Cookie == "" || !strings.HasPrefix(d.Cookie, "access_token=") {
		return fmt.Errorf("cookie must start with 'access_token='")
	}
	if d.UploadConcurrency == 0 {
		d.UploadConcurrency = 4
	}
	if d.ChunkSize == 0 {
		d.ChunkSize = 10
	}

	op.MustSaveDriverStorage(d)
	return nil
}

func (d *Teldrive) Drop(ctx context.Context) error {
	return nil
}

func (d *Teldrive) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var firstResp ListResp
	err := d.request(http.MethodGet, "/api/files", func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"path":  dir.GetPath(),
			"limit": "500",
			"page":  "1",
		})
	}, &firstResp)

	if err != nil {
		return nil, err
	}

	pagesData := make([][]Object, firstResp.Meta.TotalPages)
	pagesData[0] = firstResp.Items

	if firstResp.Meta.TotalPages > 1 {
		g, _ := errgroup.WithContext(ctx)
		g.SetLimit(8)

		for i := 2; i <= firstResp.Meta.TotalPages; i++ {
			page := i
			g.Go(func() error {
				var resp ListResp
				err := d.request(http.MethodGet, "/api/files", func(req *resty.Request) {
					req.SetQueryParams(map[string]string{
						"path":  dir.GetPath(),
						"limit": "500",
						"page":  strconv.Itoa(page),
					})
				}, &resp)

				if err != nil {
					return err
				}

				pagesData[page-1] = resp.Items
				return nil
			})
		}

		if err := g.Wait(); err != nil {
			return nil, err
		}
	}

	var allItems []Object
	for _, items := range pagesData {
		allItems = append(allItems, items...)
	}

	return utils.SliceConvert(allItems, func(src Object) (model.Obj, error) {
		return &model.Object{
			Path: path.Join(dir.GetPath(), src.Name),
			ID:   src.ID,
			Name: src.Name,
			Size: func() int64 {
				if src.Type == "folder" {
					return 0
				}
				return src.Size
			}(),
			IsFolder: src.Type == "folder",
			Modified: src.UpdatedAt,
		}, nil
	})
}

func (d *Teldrive) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.UseShareLink {
		shareObj, err := d.getShareFileById(file.GetID())
		if err != nil || shareObj == nil {
			if err := d.createShareFile(file.GetID()); err != nil {
				return nil, err
			}
			shareObj, err = d.getShareFileById(file.GetID())
			if err != nil {
				return nil, err
			}
		}
		return &model.Link{
			URL: d.Address + "/api/shares/" + url.PathEscape(shareObj.Id) + "/files/" + url.PathEscape(file.GetID()) + "/" + url.PathEscape(file.GetName()),
		}, nil
	}
	return &model.Link{
		URL: d.Address + "/api/files/" + url.PathEscape(file.GetID()) + "/" + url.PathEscape(file.GetName()),
		Header: http.Header{
			"Cookie": {d.Cookie},
		},
	}, nil
}

func (d *Teldrive) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return d.request(http.MethodPost, "/api/files/mkdir", func(req *resty.Request) {
		req.SetBody(map[string]interface{}{
			"path": parentDir.GetPath() + "/" + dirName,
		})
	}, nil)
}

func (d *Teldrive) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	body := base.Json{
		"ids":               []string{srcObj.GetID()},
		"destinationParent": dstDir.GetID(),
	}
	return d.request(http.MethodPost, "/api/files/move", func(req *resty.Request) {
		req.SetBody(body)
	}, nil)
}

func (d *Teldrive) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	body := base.Json{
		"name": newName,
	}
	return d.request(http.MethodPatch, "/api/files/{id}", func(req *resty.Request) {
		req.SetPathParam("id", srcObj.GetID())
		req.SetBody(body)
	}, nil)
}

func (d *Teldrive) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	copyConcurrentLimit := 4
	copyManager := NewCopyManager(ctx, copyConcurrentLimit, d)
	copyManager.startWorkers()
	copyManager.G.Go(func() error {
		defer close(copyManager.TaskChan)
		return copyManager.generateTasks(ctx, srcObj, dstDir)
	})
	return copyManager.G.Wait()
}

func (d *Teldrive) Remove(ctx context.Context, obj model.Obj) error {
	body := base.Json{
		"ids": []string{obj.GetID()},
	}
	return d.request(http.MethodPost, "/api/files/delete", func(req *resty.Request) {
		req.SetBody(body)
	}, nil)
}

func (d *Teldrive) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	fileId := uuid.New().String()
	chunkSizeInMB := d.ChunkSize
	chunkSize := chunkSizeInMB * 1024 * 1024 // Convert MB to bytes
	totalSize := file.GetSize()
	totalParts := int(math.Ceil(float64(totalSize) / float64(chunkSize)))
	maxRetried := 3

	// delete the upload task when finished or failed
	defer func() {
		_ = d.request(http.MethodDelete, "/api/uploads/{id}", func(req *resty.Request) {
			req.SetPathParam("id", fileId)
		}, nil)
	}()

	if obj, err := d.getFile(dstDir.GetPath(), file.GetName(), file.IsDir()); err == nil {
		if err = d.Remove(ctx, obj); err != nil {
			return err
		}
	}
	// start the upload process
	if err := d.request(http.MethodGet, "/api/uploads/fileId", func(req *resty.Request) {
		req.SetPathParam("id", fileId)
	}, nil); err != nil {
		return err
	}
	if totalSize == 0 {
		return d.touch(file.GetName(), dstDir.GetPath())
	}

	if totalParts <= 1 {
		return d.doSingleUpload(ctx, dstDir, file, up, maxRetried, totalParts, chunkSize, fileId)
	}

	return d.doMultiUpload(ctx, dstDir, file, up, maxRetried, totalParts, chunkSize, fileId)
}

func (d *Teldrive) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	// TODO get archive file meta-info, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *Teldrive) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	// TODO list args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *Teldrive) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	// TODO return link of file args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
	return nil, errs.NotImplement
}

func (d *Teldrive) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
	// TODO extract args.InnerPath path in the archive srcObj to the dstDir location, optional
	// a folder with the same name as the archive file needs to be created to store the extracted results if args.PutIntoNewDir
	// return errs.NotImplement to use an internal archive tool
	return nil, errs.NotImplement
}

//func (d *Teldrive) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*Teldrive)(nil)
