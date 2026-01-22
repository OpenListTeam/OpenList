package seafile

import (
	"context"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

type Seafile struct {
	model.Storage
	Addition

	authorization string
	root          model.Obj
}

func (d *Seafile) Config() driver.Config {
	return config
}

func (d *Seafile) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Seafile) Init(ctx context.Context) error {
	d.Address = strings.TrimSuffix(d.Address, "/")
	err := d.getToken()
	if err != nil {
		return err
	}
	d.RootFolderPath = utils.FixAndCleanPath(d.RootFolderPath)
	if d.RepoId != "" {
		library, err := d.getLibraryInfo(d.RepoId)
		if err != nil {
			return err
		}
		library.path = d.RootFolderPath
		library.ObjMask = model.Locked
		d.root = &LibraryInfo{
			LibraryItemResp: library,
		}
		return nil
	}
	if len(d.RootFolderPath) <= 1 {
		d.root = &model.Object{
			Name:     "root",
			Path:     d.RootFolderPath,
			IsFolder: true,
			Modified: d.Modified,
			Mask:     model.Locked,
		}
		return nil
	}

	var resp []LibraryItemResp
	_, err = d.request(http.MethodGet, "/api2/repos/", func(req *resty.Request) {
		req.SetResult(&resp)
	})
	if err != nil {
		return err
	}
	for _, library := range resp {
		p, found := strings.CutPrefix(d.RootFolderPath[1:], library.Name)
		if !found {
			continue
		}
		if p == "" {
			p = "/"
		} else if p[0] != '/' {
			continue
		}
		// d.RepoId = library.Id
		// d.RootFolderPath = p

		library.path = p
		library.ObjMask = model.Locked
		d.root = &LibraryInfo{
			LibraryItemResp: library,
		}
		return nil
	}
	return fmt.Errorf("Library for root folder path %q not found", d.RootFolderPath)
}

func (d *Seafile) Drop(ctx context.Context) error {
	d.root = nil
	return nil
}

func (d *Seafile) GetRoot(ctx context.Context) (model.Obj, error) {
	if d.root == nil {
		return nil, errs.StorageNotInit
	}
	return d.root, nil
}

func (d *Seafile) List(ctx context.Context, dir model.Obj, args model.ListArgs) (result []model.Obj, err error) {
	path := dir.GetPath()
	switch o := dir.(type) {
	default:
		var resp []LibraryItemResp
		_, err = d.request(http.MethodGet, "/api2/repos/", func(req *resty.Request) {
			req.SetResult(&resp)
		})
		return utils.SliceConvert(resp, func(f LibraryItemResp) (model.Obj, error) {
			f.path = path
			return &LibraryInfo{
				LibraryItemResp: f,
			}, nil
		})
	case *LibraryInfo:
		if o.Encrypted {
			err = d.decryptLibrary(o)
			if err != nil {
				return nil, err
			}
		}
	case *RepoItemResp:
		// do nothing
	}

	var resp []RepoItemResp
	_, err = d.request(http.MethodGet, fmt.Sprintf("/api2/repos/%s/dir/", dir.GetID()), func(req *resty.Request) {
		req.SetResult(&resp).SetQueryParams(map[string]string{
			"p": path,
		})
	})
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(resp, func(f RepoItemResp) (model.Obj, error) {
		f.path = stdpath.Join(path, f.Name)
		f.repoID = dir.GetID()
		return &f, nil
	})
}

func (d *Seafile) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	res, err := d.request(http.MethodGet, fmt.Sprintf("/api2/repos/%s/file/", file.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p":     file.GetPath(),
			"reuse": "1",
		})
	})
	if err != nil {
		return nil, err
	}
	u := string(res)
	u = u[1 : len(u)-1] // remove quotes
	return &model.Link{URL: u}, nil
}

func (d *Seafile) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	_, err := d.request(http.MethodPost, fmt.Sprintf("/api2/repos/%s/dir/", parentDir.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p": stdpath.Join(parentDir.GetPath(), dirName),
		}).SetFormData(map[string]string{
			"operation": "mkdir",
		})
	})
	return err
}

func (d *Seafile) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	_, err := d.request(http.MethodPost, fmt.Sprintf("/api2/repos/%s/file/", srcObj.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p": srcObj.GetPath(),
		}).SetFormData(map[string]string{
			"operation": "move",
			"dst_repo":  dstDir.GetID(),
			"dst_dir":   dstDir.GetPath(),
		})
	}, true)
	return err
}

func (d *Seafile) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	_, err := d.request(http.MethodPost, fmt.Sprintf("/api2/repos/%s/file/", srcObj.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p": srcObj.GetPath(),
		}).SetFormData(map[string]string{
			"operation": "rename",
			"newname":   newName,
		})
	}, true)
	return err
}

func (d *Seafile) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	_, err := d.request(http.MethodPost, fmt.Sprintf("/api2/repos/%s/file/", srcObj.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p": srcObj.GetPath(),
		}).SetFormData(map[string]string{
			"operation": "copy",
			"dst_repo":  dstDir.GetID(),
			"dst_dir":   dstDir.GetPath(),
		})
	})
	return err
}

func (d *Seafile) Remove(ctx context.Context, obj model.Obj) error {
	_, err := d.request(http.MethodDelete, fmt.Sprintf("/api2/repos/%s/file/", obj.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p": obj.GetPath(),
		})
	})
	return err
}

func (d *Seafile) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	res, err := d.request(http.MethodGet, fmt.Sprintf("/api2/repos/%s/upload-link/", dstDir.GetID()), func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"p": dstDir.GetPath(),
		})
	})
	if err != nil {
		return err
	}

	u := string(res)
	u = u[1 : len(u)-1] // remove quotes
	_, err = d.request(http.MethodPost, u, func(req *resty.Request) {
		r := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
			Reader:         s,
			UpdateProgress: up,
		})
		req.SetFileReader("file", s.GetName(), r).
			SetFormData(map[string]string{
				"parent_dir": dstDir.GetPath(),
				"replace":    "1",
			}).
			SetContext(ctx)
	})
	return err
}

var _ driver.Driver = (*Seafile)(nil)
