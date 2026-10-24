package intern_ai

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/pkg/errors"
)

type InternAI struct {
	model.Storage
	Addition
	apiBase string
}

func (d *InternAI) Config() driver.Config {
	return config
}

func (d *InternAI) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *InternAI) Init(ctx context.Context) error {
	d.APIBase = strings.TrimSpace(d.APIBase)
	if d.APIBase == "" {
		d.APIBase = defaultAPIBase
	}
	d.APIBase = strings.TrimSuffix(d.APIBase, "/")
	if !strings.HasPrefix(d.APIBase, "http://") && !strings.HasPrefix(d.APIBase, "https://") {
		d.APIBase = "https://" + d.APIBase
	}
	d.Token = strings.TrimSpace(d.Token)
	if d.Token == "" {
		return errors.New("token is required (uaa-token cookie of discovery.intern-ai.org.cn)")
	}
	if d.RootFolderPath == "" {
		d.RootFolderPath = "/"
	}
	d.RootFolderPath = cleanKey(d.RootFolderPath)
	d.apiBase = d.APIBase

	// Validate the token with a lightweight quota call.
	_, err := d.get(ctx, quotaEndpoint, nil)
	if err != nil {
		return errors.Wrap(err, "failed to validate token")
	}
	return nil
}

func (d *InternAI) Drop(ctx context.Context) error {
	return nil
}

func (d *InternAI) fileToObj(f fileItem, dirPath string) model.Obj {
	name := f.FileName
	if name == "" {
		name = path.Base(f.FileKey)
	}
	var modified time.Time
	if t := parseTime(f.ModifiedAt); !t.IsZero() {
		modified = t
	} else if t := parseTime(f.CreatedAt); !t.IsZero() {
		modified = t
	}
	objPath := joinKey(dirPath, name)
	return &model.Object{
		Name:     name,
		Size:     f.Size,
		Modified: modified,
		IsFolder: f.IsDir,
		Path:     objPath,
	}
}

func (d *InternAI) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.listAll(ctx, dir.GetPath(), false)
	if err != nil {
		return nil, err
	}
	objs := make([]model.Obj, 0, len(files))
	for _, f := range files {
		objs = append(objs, d.fileToObj(f, dir.GetPath()))
	}
	return objs, nil
}

func (d *InternAI) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	url, err := d.getDownloadLink(ctx, file.GetPath())
	if err != nil {
		return nil, err
	}
	return &model.Link{URL: url}, nil
}

func (d *InternAI) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	newPath := joinKey(parentDir.GetPath(), dirName)
	_, err := d.post(ctx, mkdirEndpoint, base.Json{
		"dir_path": cleanKey(parentDir.GetPath()),
		"dir_name": dirName,
	})
	if err != nil {
		return nil, err
	}
	return &model.Object{
		Name:     dirName,
		Path:     newPath,
		IsFolder: true,
		Modified: time.Now(),
	}, nil
}

func (d *InternAI) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	_, err := d.post(ctx, moveEndpoint, base.Json{
		"src_keys":          []string{cleanKey(srcObj.GetPath())},
		"dst_dir":           cleanKey(dstDir.GetPath()),
		"conflict_strategy": "overwrite",
	})
	if err != nil {
		return nil, err
	}
	return &model.Object{
		Name:     srcObj.GetName(),
		Path:     joinKey(dstDir.GetPath(), srcObj.GetName()),
		IsFolder: srcObj.IsDir(),
		Modified: srcObj.ModTime(),
		Size:     srcObj.GetSize(),
	}, nil
}

func (d *InternAI) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	_, err := d.post(ctx, renameEndpoint, base.Json{
		"src_key":           cleanKey(srcObj.GetPath()),
		"new_name":          newName,
		"conflict_strategy": "overwrite",
	})
	if err != nil {
		return nil, err
	}
	return &model.Object{
		Name:     newName,
		Path:     joinKey(path.Dir(srcObj.GetPath()), newName),
		IsFolder: srcObj.IsDir(),
		Modified: srcObj.ModTime(),
		Size:     srcObj.GetSize(),
	}, nil
}

func (d *InternAI) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	_, err := d.post(ctx, copyEndpoint, base.Json{
		"src_keys":          []string{cleanKey(srcObj.GetPath())},
		"dst_dir":           cleanKey(dstDir.GetPath()),
		"conflict_strategy": "overwrite",
	})
	if err != nil {
		return nil, err
	}
	return &model.Object{
		Name:     srcObj.GetName(),
		Path:     joinKey(dstDir.GetPath(), srcObj.GetName()),
		IsFolder: srcObj.IsDir(),
		Modified: srcObj.ModTime(),
		Size:     srcObj.GetSize(),
	}, nil
}

func (d *InternAI) Remove(ctx context.Context, obj model.Obj) error {
	_, err := d.post(ctx, deleteEndpoint, base.Json{
		"file_keys": []string{cleanKey(obj.GetPath())},
	})
	return err
}

func (d *InternAI) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	newPath := joinKey(dstDir.GetPath(), file.GetName())
	resp, err := d.post(ctx, uploadURLPath, base.Json{
		"dir_path": cleanKey(dstDir.GetPath()),
		"files": []base.Json{{
			"file_name":         file.GetName(),
			"file_size":         file.GetSize(),
			"file_type":         fileExt(file.GetName()),
			"conflict_strategy": "overwrite",
		}},
	})
	if err != nil {
		return nil, err
	}
	var uploadURL string
	for _, u := range resp.Data.URLs {
		if u.Skipped {
			continue
		}
		if u.UploadURL != "" {
			uploadURL = u.UploadURL
			break
		}
	}
	if uploadURL == "" {
		return nil, errors.Errorf("no upload url for %s", newPath)
	}

	// PUT the file content to the presigned URL.
	r, err := base.RestyClient.R().
		SetContext(ctx).
		SetHeader("Content-Type", "application/octet-stream").
		SetBody(driver.NewLimitedUploadStream(ctx, file)).
		SetDoNotParseResponse(true).
		Put(uploadURL)
	if err != nil {
		return nil, errors.Wrap(err, "failed to upload file")
	}
	defer r.RawResponse.Body.Close()
	if r.IsError() {
		return nil, fmt.Errorf("upload returned HTTP %d", r.StatusCode())
	}
	up(100)

	return &model.Object{
		Name:     file.GetName(),
		Path:     newPath,
		Size:     file.GetSize(),
		Modified: file.ModTime(),
		IsFolder: false,
	}, nil
}

var _ driver.Driver = (*InternAI)(nil)
