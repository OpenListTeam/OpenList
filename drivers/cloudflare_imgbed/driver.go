package cloudflare_imgbed

import (
	"context"
	"fmt"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
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
	d.Address = strings.TrimRight(d.Address, "/")
	d.client = base.NewRestyClient()
	d.client.SetBaseURL(d.Address).
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

// List 获取指定目录下的文件和子目录列表。
//
// 采用内部分页循环拉取，以防止单目录文件过多导致 API 响应超时或内存异常。
// 每次请求 listPageSize 条记录，直到返回数量不足一页时退出循环，
// 最终将所有分页结果汇总后一次性返回给 OpenList。
func (d *CFImgBed) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	reqPath := dir.GetPath()

	// 用于去重：API 在分页时每个页面都可能重复返回相同的目录列表，
	// 确保同一个目录对象只被添加一次。
	dirSeen := ":"
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
			p := strings.TrimRight(rawDir, "/")
			// 目录去重：分页场景下不同页面可能返回相同的目录条目
			if !strings.Contains(dirSeen, ":"+p+":") {
				dirSeen += p + ":"
				name := stdpath.Base(p)
				objs = append(objs, &model.Object{
					Name:     name,
					Path:     stdpath.Join(reqPath, name),
					Modified: d.Modified,
					IsFolder: true,
				})
			}
		}

		for _, item := range resp.Files {
			obj := parseFile(item)
			obj.Path = stdpath.Join(reqPath, obj.Name)
			objs = append(objs, obj)
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

// Link 拼装文件的直接下载/访问链接。
// 路径中可能包含空格、中文、#、+ 等特殊字符，必须进行安全编码以生成有效 URL。
func (d *CFImgBed) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	// 对路径进行安全编码，处理空格、特殊字符等可能导致链接失效的情况
	link := d.Address + "/file/" + utils.EncodePath(file.GetPath())
	return &model.Link{URL: link}, nil
}

// 编译时检查 CFImgBed 是否完整实现 driver.Driver 接口。
var _ driver.Driver = (*CFImgBed)(nil)
