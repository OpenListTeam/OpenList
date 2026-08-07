package cloudflare_imgbed

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type CFImgBed struct {
	model.Storage
	Addition
	client          *resty.Client
	virtualDir      *cache.WeakCacheMap[string, model.Object]
	publicUrlPrefix string
}

func (d *CFImgBed) Config() driver.Config          { return config }
func (d *CFImgBed) GetAddition() driver.Additional { return &d.Addition }

func (d *CFImgBed) Init(ctx context.Context) error {
	d.UploadThread = min(d.UploadThread, 32)
	if d.UploadThread < 1 {
		d.UploadThread = 3
	}
	d.Address = strings.TrimRight(d.Address, "/")

	d.client = base.NewRestyClient().
		SetBaseURL(d.Address).
		SetHeader("Authorization", "Bearer "+d.Token).
		SetDebug(false)

	// 连通性测试：尝试获取根目录单条数据
	_, err := d.doRequest(ctx, http.MethodGet, listApi, func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"start": "0",
			"count": "1",
			"dir":   "/",
		})
	}, nil)
	if err != nil {
		return fmt.Errorf("init verification failed: %w", err)
	}
	d.virtualDir = cache.NewWeakCacheMap[string, model.Object]()
	return nil
}

func (d *CFImgBed) Drop(ctx context.Context) error {
	if d.virtualDir != nil {
		d.virtualDir.Clear()
	}
	return nil
}

func (d *CFImgBed) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	// if !args.Refresh && model.ObjHasMask(dir, model.Virtual) {
	//      if _, ok := d.virtualDir.Load(dir.GetPath()); ok {
	//              return nil, nil
	//      }
	// }

	var dirSeen map[string]bool
	var fileSeen map[string]bool
	var objs []model.Obj

	start := 0
	for {
		var resp ListResponse
		_, err := d.doRequest(ctx, http.MethodGet, listApi, func(req *resty.Request) {
			req.SetQueryParams(map[string]string{
				"dir":   dir.GetPath(),
				"start": fmt.Sprintf("%d", start),
				"count": fmt.Sprintf("%d", listPageSize),
			})
		}, &resp)
		if err != nil {
			return nil, err
		}
		if len(resp.Files) == 0 && len(resp.Directories) == 0 {
			break
		}

		if start == 0 {
			dirSeen = make(map[string]bool, len(resp.Directories))
			fileSeen = make(map[string]bool, len(resp.Files))
			objs = make([]model.Obj, 0, len(resp.Directories)+len(resp.Files))
		}

		for _, rawDir := range resp.Directories {
			rawDir = "/" + strings.TrimRight(rawDir, "/")
			if !dirSeen[rawDir] {
				dirSeen[rawDir] = true
				objs = append(objs, &model.Object{
					Path:     rawDir,
					Name:     path.Base(rawDir),
					Modified: d.Modified,
					IsFolder: true,
				})
			}
		}

		for _, item := range resp.Files {
			if !fileSeen[item.Name] {
				fileSeen[item.Name] = true
				objs = append(objs, parseFile(item))
			}
		}

		// 如果当前获取的数量少于分页大小，说明已加载完毕
		if len(resp.Files)+len(resp.Directories) < listPageSize {
			break
		}
		start += listPageSize
	}
	return objs, nil
}

func (d *CFImgBed) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if d.publicUrlPrefix != "" {
		return &model.Link{URL: d.publicUrlPrefix + utils.EncodePath(file.GetPath())}, nil
	}
	return &model.Link{URL: d.Address + "/file" + utils.EncodePath(file.GetPath())}, nil
}

func (d *CFImgBed) Get(ctx context.Context, pathStr string) (model.Obj, error) {
	fullPath := path.Join(d.RootFolderPath, pathStr)
	if obj, found := d.virtualDir.Load(fullPath); found {
		return obj, nil
	}
	return nil, errs.NotSupport
}

// MakeDir 在图床中通常是虚拟的，此处返回虚拟目录对象以支持上传时的路径展示
func (d *CFImgBed) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	fullPath := path.Join(parentDir.GetPath(), dirName)
	temp := &model.Object{
		Path:     fullPath,
		Name:     dirName,
		IsFolder: true,
		Modified: d.Modified,
		Mask:     model.Virtual,
	}
	d.virtualDir.Store(fullPath, temp)
	return temp, nil
}

func (d *CFImgBed) Remove(ctx context.Context, obj model.Obj) error {
	reqPath := obj.GetPath()
	if model.ObjHasMask(obj, model.Virtual) {
		d.virtualDir.Delete(reqPath)
		return nil
	}
	_, err := d.doRequest(ctx, http.MethodPost, deleteApi+utils.EncodePath(reqPath), func(req *resty.Request) {
		req.SetQueryParam("folder", fmt.Sprintf("%t", obj.IsDir()))
	}, nil)
	return err
}

var _ driver.Driver = (*CFImgBed)(nil)
