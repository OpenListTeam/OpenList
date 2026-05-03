package cloudflare_imgbed

import (
	"context"
	"fmt"
	"net/http"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type CFImgBed struct {
	model.Storage
	Addition
	client *resty.Client
}

func (d *CFImgBed) Config() driver.Config         { return config }
func (d *CFImgBed) GetAddition() driver.Additional { return &d.Addition }

func (d *CFImgBed) Init(ctx context.Context) error {
	if d.UploadThread <= 0 || d.UploadThread > 32 {
		d.UploadThread = 3
	}

	d.client = base.NewRestyClient().
		SetBaseURL(strings.TrimRight(d.Address, "/")).
		SetHeader("Authorization", "Bearer "+d.Token).
		SetDebug(false)

	// 连通性测试：尝试获取根目录单条数据
	_, err := d.doRequest(http.MethodGet, ListApi, func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"start": "0",
			"count": "1",
			"dir":   "/",
		})
	}, nil)
	if err != nil {
		return fmt.Errorf("init verification failed: %w", err)
	}
	log.Info("Cloudflare ImgBed driver initialized successfully")
	return nil
}

func (d *CFImgBed) Drop(ctx context.Context) error { return nil }

// buildReqPath 拼接存储根路径与业务请求路径，确保生成的路径符合 API 预期
func buildReqPath(rootPath, dirPath string) string {
	rootPath = strings.Trim(rootPath, "/")
	dirPath = strings.Trim(dirPath, "/")
	if dirPath == "" || dirPath == rootPath {
		return rootPath
	}
	if rootPath == "" {
		return dirPath
	}
	return rootPath + "/" + dirPath
}

func (d *CFImgBed) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	rootPath := strings.Trim(d.GetRootPath(), "/")
	var dirPath string
	if dir != nil {
		dirPath = strings.Trim(dir.GetPath(), "/")
	}
	reqPath := buildReqPath(rootPath, dirPath)

	dirSeen := make(map[string]bool)
	fileSeen := make(map[string]bool)
	objs := make([]model.Obj, 0)

	start := 0
	for {
		var resp ListResponse
		_, err := d.doRequest(http.MethodGet, ListApi, func(req *resty.Request) {
			req.SetQueryParams(map[string]string{
				"dir":   reqPath,
				"start": fmt.Sprintf("%d", start),
				"count": fmt.Sprintf("%d", listPageSize),
			})
		}, &resp)
		if err != nil {
			return nil, err
		}

		for _, rawDir := range resp.Directories {
			cleanDir := strings.TrimRight(rawDir, "/")
			p := stripRootPrefix(cleanDir, rootPath)
			if !dirSeen[p] {
				dirSeen[p] = true
				objs = append(objs, parseDir(p))
			}
		}

		for _, item := range resp.Files {
			p := stripRootPrefix(item.Name, rootPath)
			if !fileSeen[p] {
				fileSeen[p] = true
				objs = append(objs, parseFile(FileItem{Name: p, Metadata: item.Metadata}))
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
	rootPath := strings.Trim(d.GetRootPath(), "/")
	filePath := strings.Trim(file.GetPath(), "/")

	var fullPath string
	if rootPath != "" && filePath != "" {
		fullPath = rootPath + "/" + filePath
	} else if rootPath != "" {
		fullPath = rootPath
	} else {
		fullPath = filePath
	}

	link := strings.TrimRight(d.Address, "/") + "/file/" + utils.EncodePath(fullPath)
	return &model.Link{URL: link}, nil
}

// MakeDir 在图床中通常是虚拟的，此处返回虚拟目录对象以支持上传时的路径展示
func (d *CFImgBed) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	var parentPath string
	if parentDir != nil {
		parentPath = parentDir.GetPath()
	}
	fullPath := path.Join(parentPath, dirName)
	return &model.Object{
		ID:       fullPath,
		Path:     fullPath,
		Name:     dirName,
		IsFolder: true,
	}, nil
}

func (d *CFImgBed) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, errs.NotImplement
}
func (d *CFImgBed) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	return nil, errs.NotImplement
}
func (d *CFImgBed) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return nil, errs.NotImplement
}
func (d *CFImgBed) Remove(ctx context.Context, obj model.Obj) error { return errs.NotImplement }
func (d *CFImgBed) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return nil, errs.NotImplement
}

var _ driver.Driver = (*CFImgBed)(nil)