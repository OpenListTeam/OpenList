package cloudflare_imgbed

import (
        "context"
        "fmt"
        "strings"

        "github.com/OpenListTeam/OpenList/v4/drivers/base"
        "github.com/OpenListTeam/OpenList/v4/internal/driver"
        "github.com/OpenListTeam/OpenList/v4/internal/errs"
        "github.com/OpenListTeam/OpenList/v4/internal/model"
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

// Init initializes the HTTP client with the configured Address and Token.
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

// apiError represents a generic error response from the CFImgBed API.
type apiError struct {
        Error   string `json:"error"`
        Message string `json:"message"`
}

// buildReqPath constructs the path to send to the CFImgBed List API.
//
// OpenList may call List() in two ways:
//  1. List(nil) — initial load of the mount root
//  2. List(obj) — where obj was returned by a previous List() call
//
// When RootPath is set (e.g. "/telegram"), OpenList may pass a virtual root
// dir object whose GetPath() already equals the root path itself. We must
// detect this and avoid double-prepending rootPath.
func buildReqPath(rootPath, dirPath string) string {
        rootPath = strings.Trim(rootPath, "/")
        dirPath = strings.Trim(dirPath, "/")

        if dirPath == "" || dirPath == rootPath {
                // Either listing the real root, or OpenList passed the virtual root dir
                return rootPath
        }
        if rootPath == "" {
                return dirPath
        }
        // dirPath is a subfolder returned by a previous List call, prepend rootPath
        return rootPath + "/" + dirPath
}

// List retrieves the file and directory listing for the given directory.
func (d *CFImgBed) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
        rootPath := strings.Trim(d.GetRootPath(), "/")

        var dirPath string
        if dir != nil {
                dirPath = strings.Trim(dir.GetPath(), "/")
        }
        reqPath := buildReqPath(rootPath, dirPath)

        var resp ListResponse
        var errResp apiError
        res, err := d.client.R().
                SetQueryParam("dir", reqPath).
                SetQueryParam("count", "-1").
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

        objs := make([]model.Obj, 0, len(resp.Directories)+len(resp.Files))

        // Strip rootPath prefix from returned paths so that GetPath() is relative
        // to the OpenList mount point, not the CFImgBed root.
        for _, rawDir := range resp.Directories {
                cleanDir := strings.TrimRight(rawDir, "/")
                p := stripRootPrefix(cleanDir, rootPath)
                objs = append(objs, parseDir(p))
        }

        for _, item := range resp.Files {
                p := stripRootPrefix(item.Name, rootPath)
                objs = append(objs, parseFile(FileItem{
                        Name:     p,
                        Metadata: item.Metadata,
                }))
        }

        return objs, nil
}

// stripRootPrefix removes the rootPath prefix from a path returned by the API.
// If rootPath is empty or the path doesn't start with rootPath/, return as-is.
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

// Link constructs a direct download URL for the given file object.
// Format: {Address}/file/{rootPath}/{filePath} with no double slashes.
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

        link := strings.TrimRight(d.Address, "/") + "/file/" + fullPath
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

var _ driver.Driver = (*CFImgBed)(nil)
