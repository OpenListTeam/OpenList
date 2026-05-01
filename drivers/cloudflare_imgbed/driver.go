package cloudflare_imgbed

import (
	"context"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type CFImgBed struct {
	model.Storage
	Addition
	client *resty.Client
}

func (d *CFImgBed) Config() driver.Config {
	return config
}

func (d *CFImgBed) GetAddition() driver.Additional {
	return &d.Addition
}

// Init 使用 base 包提供的工厂方法初始化 HTTP 客户端，
// 并设置 API 基础地址和鉴权请求头。
func (d *CFImgBed) Init(ctx context.Context) error {
	d.client = base.NewRestyClient()
	d.client.SetBaseURL(strings.TrimRight(d.Address, "/")).
		SetHeader("Authorization", "Bearer "+d.Token).
		SetDebug(false)
	return nil
}

func (d *CFImgBed) Drop(ctx context.Context) error {
	return nil
}

// apiError 表示 CFImgBed API 返回的通用错误响应结构。
type apiError struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

// buildReqPath 根据挂载根路径和当前浏览目录，拼接出发送给 API 的请求路径。
//
// OpenList 可能在两种场景下调用 List：
//  1. List(nil) — 首次加载挂载点根目录
//  2. List(obj) — 用户点击进入某个子目录，obj 由上一次 List 返回
//
// 当设置了 RootPath（如 "/telegram"）时，OpenList 首次调用的 dir 对象
// 的 GetPath() 可能已经等于 rootPath 本身，此时不应重复拼接前缀。
func buildReqPath(rootPath, dirPath string) string {
	rootPath = strings.Trim(rootPath, "/")
	dirPath = strings.Trim(dirPath, "/")

	if dirPath == "" || dirPath == rootPath {
		// 正在浏览根目录，或 OpenList 传入了虚拟根目录对象
		return rootPath
	}
	if rootPath == "" {
		// 未设置挂载前缀，直接使用目录路径
		return dirPath
	}
	// 正常子目录：在目录路径前补上挂载根路径
	return rootPath + "/" + dirPath
}

// List 获取指定目录下的文件和子目录列表。
//
// 采用内部分页循环拉取，以防止单目录文件过多导致 API 响应超时或内存异常。
// 每次请求 listPageSize 条记录，直到返回数量不足一页时退出循环，
// 最终将所有分页结果汇总后一次性返回给 OpenList。
func (d *CFImgBed) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	rootPath := strings.Trim(d.GetRootPath(), "/")

	var dirPath string
	if dir != nil {
		dirPath = strings.Trim(dir.GetPath(), "/")
	}
	reqPath := buildReqPath(rootPath, dirPath)

	// 用于去重：API 在分页时每个页面都可能重复返回相同的目录列表，
	// 使用 map 确保同一个目录对象只被添加一次。
	dirSeen := make(map[string]bool)
	objs := make([]model.Obj, 0)

	// 分页拉取循环
	start := 0
	for {
		var resp ListResponse
		var errResp apiError
		res, err := d.client.R().
			SetQueryParam("dir", reqPath).
			SetQueryParam("start", fmt.Sprintf("%d", start)).
			SetQueryParam("count", fmt.Sprintf("%d", listPageSize)).
			SetResult(&resp).
			SetError(&errResp).
			Get("/api/manage/list")

		if err != nil {
			return nil, err
		}
		if res.IsError() {
			if errResp.Message != "" {
				return nil, fmt.Errorf("CFImgBed API error: %s", errResp.Message)
			}
			return nil, fmt.Errorf("CFImgBed API returned status %d", res.StatusCode())
		}

		// 裁剪 API 返回路径中的挂载根前缀，
		// 使 GetPath() 返回的是相对于 OpenList 挂载点的路径，而非图床的绝对路径。
		for _, rawDir := range resp.Directories {
			cleanDir := strings.TrimRight(rawDir, "/")
			p := stripRootPrefix(cleanDir, rootPath)
			// 目录去重：分页场景下不同页面可能返回相同的目录条目
			if !dirSeen[p] {
				dirSeen[p] = true
				objs = append(objs, parseDir(p))
			}
		}

		for _, item := range resp.Files {
			p := stripRootPrefix(item.Name, rootPath)
			objs = append(objs, parseFile(FileItem{
				Name:     p,
				Metadata: item.Metadata,
			}))
		}

		// 判断是否已到最后一页：当返回的文件和目录总数小于请求的每页数量时，
		// 说明本页已经是最后一页，无需继续请求。
		fetched := len(resp.Files) + len(resp.Directories)
		if fetched < listPageSize {
			break
		}

		start += listPageSize
	}

	return objs, nil
}

// stripRootPrefix 移除 API 返回路径中的挂载根前缀。
// 如果未设置 rootPath 或路径不以 rootPath/ 开头，则原样返回。
func stripRootPrefix(p, rootPath string) string {
	if rootPath == "" {
		return p
	}
	prefix := rootPath + "/"
	if strings.HasPrefix(p, prefix) {
		return strings.TrimPrefix(p, prefix)
	}
	return p
}

// Link 拼装文件的直接下载/访问链接。
// 路径中可能包含空格、中文、#、+ 等特殊字符，必须进行安全编码以生成有效 URL。
func (d *CFImgBed) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	rootPath := strings.Trim(d.GetRootPath(), "/")
	filePath := strings.Trim(file.GetPath(), "/")

	// 拼接完整路径，避免出现双斜杠
	var fullPath string
	if rootPath != "" && filePath != "" {
		fullPath = rootPath + "/" + filePath
	} else if rootPath != "" {
		fullPath = rootPath
	} else {
		fullPath = filePath
	}

	// 对路径进行安全编码，处理空格、特殊字符等可能导致链接失效的情况
	link := strings.TrimRight(d.Address, "/") + "/file/" + utils.EncodePath(fullPath)
	return &model.Link{URL: link}, nil
}

func (d *CFImgBed) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	return nil, errs.NotImplement
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

func (d *CFImgBed) Remove(ctx context.Context, obj model.Obj) error {
	return errs.NotImplement
}

func (d *CFImgBed) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CFImgBed) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	return nil, errs.NotImplement
}

func (d *CFImgBed) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CFImgBed) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func (d *CFImgBed) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
	return nil, errs.NotImplement
}

func (d *CFImgBed) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	return nil, errs.NotImplement
}

// 编译时检查 CFImgBed 是否完整实现 driver.Driver 接口。
var _ driver.Driver = (*CFImgBed)(nil)
