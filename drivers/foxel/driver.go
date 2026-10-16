package foxel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Foxel struct {
	model.Storage
	Addition

	client      *http.Client
	tokenMu     sync.RWMutex
	loginMu     sync.Mutex
	accessToken string
}

func (d *Foxel) Config() driver.Config {
	return config
}

func (d *Foxel) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Foxel) Init(ctx context.Context) error {
	d.Address = strings.TrimRight(strings.TrimSpace(d.Address), "/")
	u, err := url.Parse(d.Address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("Foxel: url must be an HTTP(S) server URL without credentials, query or fragment")
	}
	if d.PageSize == 0 {
		d.PageSize = 200
	}
	if d.PageSize < 1 || d.PageSize > 500 {
		return fmt.Errorf("Foxel: page_size must be between 1 and 500")
	}
	if d.LinkExpire == 0 {
		d.LinkExpire = 3600
	}
	if d.LinkExpire < 0 || int64(d.LinkExpire) > int64((1<<63-1)/time.Second) {
		return fmt.Errorf("Foxel: link_expire must be a positive lifetime in seconds")
	}
	d.RootFolderPath = path.Clean("/" + strings.TrimPrefix(d.RootFolderPath, "/"))
	token := strings.TrimSpace(d.Token)
	if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if (d.Username == "") != (d.Password == "") {
		return fmt.Errorf("Foxel: username and password must be provided together")
	}
	if token == "" && d.Username == "" {
		return fmt.Errorf("Foxel: provide a token or username and password")
	}
	if d.client == nil {
		d.client = base.HttpClient
	}
	if d.client == nil {
		return fmt.Errorf("Foxel: HTTP client is not initialized")
	}
	// Credentials take precedence over a manually configured token.
	if d.Username != "" {
		token = ""
	}
	d.setToken(token)
	return d.request(ctx, http.MethodGet, "/auth/me", nil, nil, nil)
}

func (d *Foxel) Drop(ctx context.Context) error {
	d.setToken("")
	return nil
}

func (d *Foxel) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files := make([]model.Obj, 0)
	seen := make(map[string]bool)
	cursors := make(map[string]bool)
	pageNum, cursor := 1, ""
	for {
		query := url.Values{
			"page": {strconv.Itoa(pageNum)}, "page_size": {strconv.Itoa(d.PageSize)},
			"sort_by": {"name"}, "sort_order": {"asc"},
		}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var result listing
		if err := d.request(ctx, http.MethodGet, fsAPI("", dir.GetPath()), query, nil, &result); err != nil {
			return nil, err
		}
		for _, item := range result.Entries {
			fullPath, err := childPath(dir.GetPath(), item.Name)
			if err != nil {
				return nil, err
			}
			if seen[fullPath] {
				continue
			}
			seen[fullPath] = true
			files = append(files, &model.Object{
				ID: fullPath, Path: fullPath, Name: item.Name, Size: item.Size,
				Modified: time.Unix(item.Mtime, 0), IsFolder: item.IsDir,
			})
		}
		pagination := result.Pagination
		switch pagination.Mode {
		case "cursor":
			if !pagination.HasNext {
				return files, nil
			}
			if pagination.NextCursor == "" || cursors[pagination.NextCursor] {
				return nil, fmt.Errorf("Foxel: directory listing returned an empty or repeated cursor")
			}
			cursor = pagination.NextCursor
			cursors[cursor] = true
		case "", "paged":
			pageSize := pagination.PageSize
			if pageSize <= 0 {
				pageSize = d.PageSize
			}
			if pagination.Pages != nil {
				if pageNum >= *pagination.Pages {
					return files, nil
				}
			} else if pagination.Total != nil {
				if pageNum*pageSize >= *pagination.Total {
					return files, nil
				}
			} else if len(result.Entries) < pageSize {
				return files, nil
			}
			pageNum++
		default:
			return nil, fmt.Errorf("Foxel: unsupported pagination mode %q", pagination.Mode)
		}
	}
}

func (d *Foxel) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var result tempLink
	query := url.Values{"expires_in": {strconv.Itoa(d.LinkExpire)}}
	if err := d.request(ctx, http.MethodGet, fsAPI("temp-link", file.GetPath()), query, nil, &result); err != nil {
		return nil, err
	}
	downloadURL, err := d.downloadURL(result, file.GetName())
	if err != nil {
		return nil, err
	}
	lifetime := time.Duration(d.LinkExpire) * time.Second
	expiration := lifetime - min(lifetime/10, 30*time.Second)
	return &model.Link{URL: downloadURL, Expiration: &expiration}, nil
}

func (d *Foxel) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	fullPath, err := childPath(parentDir.GetPath(), dirName)
	if err != nil {
		return err
	}
	return d.request(ctx, http.MethodPost, "/fs/mkdir", nil, map[string]string{"path": fullPath}, nil)
}

func (d *Foxel) transfer(ctx context.Context, operation, src, dst string) error {
	var result transferResult
	if err := d.request(ctx, http.MethodPost, "/fs/"+operation, url.Values{"overwrite": {"false"}},
		map[string]string{"src": src, "dst": dst}, &result); err != nil {
		return err
	}
	if result.Queued {
		return d.waitTask(ctx, result.TaskID)
	}
	if (operation == "move" && result.Moved) || (operation == "copy" && result.Copied) || (operation == "rename" && result.Renamed) {
		return nil
	}
	return fmt.Errorf("Foxel: %s was neither completed nor queued", operation)
}

func (d *Foxel) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	dst, err := childPath(dstDir.GetPath(), srcObj.GetName())
	if err != nil {
		return err
	}
	return d.transfer(ctx, "move", srcObj.GetPath(), dst)
}

func (d *Foxel) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	dst, err := childPath(path.Dir(srcObj.GetPath()), newName)
	if err != nil {
		return err
	}
	return d.transfer(ctx, "rename", srcObj.GetPath(), dst)
}

func (d *Foxel) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	dst, err := childPath(dstDir.GetPath(), srcObj.GetName())
	if err != nil {
		return err
	}
	return d.transfer(ctx, "copy", srcObj.GetPath(), dst)
}

func (d *Foxel) Remove(ctx context.Context, obj model.Obj) error {
	return d.request(ctx, http.MethodDelete, fsAPI("", obj.GetPath()), nil, nil, nil)
}

func (d *Foxel) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fullPath, err := childPath(dstDir.GetPath(), file.GetName())
	if err != nil {
		return nil, err
	}
	// Refresh an expired session before consuming a possibly non-seekable upload stream.
	if err = d.request(ctx, http.MethodGet, "/auth/me", nil, nil, nil); err != nil {
		return nil, err
	}
	var reader io.Reader = file
	if file.GetSize() > 0 && up != nil {
		reader = &driver.ReaderUpdatingProgress{Reader: file, UpdateProgress: up}
	}
	reader = driver.NewLimitedUploadStream(ctx, reader)
	res, err := d.send(ctx, http.MethodPut, fsAPI("upload-raw", fullPath), url.Values{"overwrite": {"true"}},
		reader, "application/octet-stream", file.GetSize(), d.currentToken())
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var result uploadResult
	if err = decodeResponse(res, &result); err != nil {
		return nil, err
	}
	if !result.Uploaded {
		return nil, fmt.Errorf("Foxel: upload was not completed")
	}
	if result.Path != "" {
		fullPath = result.Path
	}
	if up != nil {
		up(100)
	}
	return &model.Object{
		ID: fullPath, Path: fullPath, Name: path.Base(fullPath), Size: result.Size,
		Modified: file.ModTime(), Ctime: file.CreateTime(),
	}, nil
}

var (
	_ driver.Driver    = (*Foxel)(nil)
	_ driver.Mkdir     = (*Foxel)(nil)
	_ driver.Move      = (*Foxel)(nil)
	_ driver.Rename    = (*Foxel)(nil)
	_ driver.Copy      = (*Foxel)(nil)
	_ driver.Remove    = (*Foxel)(nil)
	_ driver.PutResult = (*Foxel)(nil)
)
