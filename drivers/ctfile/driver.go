package ctfile

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// CtFile implements driver.Driver for 城通网盘 (CtFile) via the OpenAPI
// session-based authentication. No email/password/SMS login is used.
type CtFile struct {
	model.Storage
	Addition
}

func (d *CtFile) Config() driver.Config {
	return config
}

func (d *CtFile) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *CtFile) Init(ctx context.Context) error {
	if strings.TrimSpace(d.Session) == "" {
		return fmt.Errorf("[CtFile] session is required; generate one in the CtFile backend under 「开放接口登录密钥管理」")
	}
	return d.profile(ctx)
}

func (d *CtFile) Drop(ctx context.Context) error {
	return nil
}

// List returns the contents of dir (both sub-folders and files).
func (d *CtFile) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	folderID := dir.GetID()
	if folderID == "" {
		folderID = rootFolderID
	}

	folders, err := d.listFolders(ctx, folderID)
	if err != nil {
		return nil, err
	}
	files, err := d.listFiles(ctx, folderID)
	if err != nil {
		return nil, err
	}

	var objs []model.Obj
	for _, f := range folders {
		objs = append(objs, &model.Object{
			ID:       f.Key,
			Name:     f.Name,
			IsFolder: true,
			Modified: parseDate(f.Date),
			Ctime:    parseDate(f.Date),
		})
	}
	for _, f := range files {
		objs = append(objs, &model.Object{
			ID:       f.Key,
			Name:     f.Name,
			Size:     f.Size,
			IsFolder: false,
			Modified: parseDate(f.Date),
			Ctime:    parseDate(f.Date),
		})
	}
	return objs, nil
}

// Link fetches the CDN download URL for file via fetch_url.
func (d *CtFile) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	url, err := d.fetchURL(ctx, file.GetID())
	if err != nil {
		return nil, err
	}
	return &model.Link{
		URL: url,
		Header: map[string][]string{
			"User-Agent": {d.ua()},
		},
	}, nil
}

// MakeDir creates a sub-folder under parentDir.
func (d *CtFile) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	parentID := parentDir.GetID()
	id, err := d.mkdir(ctx, parentID, dirName)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       id,
		Name:     dirName,
		IsFolder: true,
		Modified: time.Now(),
		Ctime:    time.Now(),
	}, nil
}

// Move relocates srcObj into dstDir.
func (d *CtFile) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	err := d.move(ctx, []string{srcObj.GetID()}, dstDir.GetID())
	if err != nil {
		return nil, err
	}
	return srcObj, nil
}

// Rename changes the name of srcObj to newName.
func (d *CtFile) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	var err error
	if srcObj.IsDir() {
		err = d.renameFolder(ctx, srcObj.GetID(), newName)
	} else {
		err = d.renameFile(ctx, srcObj.GetID(), newName)
	}
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       srcObj.GetID(),
		Name:     newName,
		Size:     srcObj.GetSize(),
		IsFolder: srcObj.IsDir(),
		Modified: srcObj.ModTime(),
		Ctime:    srcObj.CreateTime(),
	}, nil
}

// Copy copies srcObj into dstDir.
// The spec explicitly requires implementation; the Worker's "unimplemented"
// note is overridden here. The API endpoint is POST /{ns}/file/copy.
func (d *CtFile) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return d.copyFiles(ctx, []string{srcObj.GetID()}, dstDir.GetID())
}

// Remove deletes obj (moves to trash).
func (d *CtFile) Remove(ctx context.Context, obj model.Obj) error {
	return d.remove(ctx, []string{obj.GetID()})
}

// Put uploads a file to dstDir. The stream is cached so the checksum and the
// subsequent multipart upload can reuse it without keeping the entire file in memory.
func (d *CtFile) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	md5hex := file.GetHash().GetHash(utils.MD5)
	tmpFile := file.GetFile()
	var err error
	var cacheProgress *model.UpdateProgress
	if up != nil {
		cacheProgress = &up
	}

	if len(md5hex) != utils.MD5.Width {
		// Cache the full stream and compute MD5 simultaneously.
		tmpFile, md5hex, err = stream.CacheFullAndHash(file, cacheProgress, utils.MD5)
	} else if tmpFile == nil {
		// A pre-computed MD5 still needs a seekable source for the upload.
		tmpFile, err = file.CacheFullAndWriter(cacheProgress, nil)
	}
	if err != nil {
		return err
	}
	if tmpFile == nil {
		return fmt.Errorf("[CtFile] failed to cache upload stream")
	}
	if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
		return err
	}

	size := file.GetSize()
	if size < 0 {
		size, err = tmpFile.Seek(0, io.SeekEnd)
		if err != nil {
			return err
		}
		if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}

	if up != nil {
		up(50)
	}

	parentID := dstDir.GetID()
	err = d.uploadFile(ctx, parentID, file.GetName(), tmpFile, size, md5hex)
	if err != nil {
		return err
	}

	if up != nil {
		up(100)
	}
	return nil
}

// parseDate converts a CtFile date value (unix seconds or "YYYY-MM-DD HH:mm:ss")
// to time.Time, falling back to time.Now() on failure.
func parseDate(v any) time.Time {
	if v == nil {
		return time.Now()
	}
	switch tv := v.(type) {
	case float64:
		if tv > 0 {
			return time.Unix(int64(tv), 0)
		}
	case string:
		if tv == "" {
			return time.Now()
		}
		// Try unix seconds as string
		if n, err := parseInt64(tv); err == nil && n > 0 {
			return time.Unix(n, 0)
		}
		// Try "YYYY-MM-DD HH:mm:ss"
		layouts := []string{"2006-01-02 15:04:05", "2006-01-02T15:04:05Z"}
		for _, layout := range layouts {
			if t, err := time.Parse(layout, tv); err == nil {
				return t
			}
		}
	}
	return time.Now()
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

var (
	_ driver.Driver      = (*CtFile)(nil)
	_ driver.MkdirResult = (*CtFile)(nil)
	_ driver.MoveResult  = (*CtFile)(nil)
	_ driver.RenameResult = (*CtFile)(nil)
	_ driver.Copy        = (*CtFile)(nil)
	_ driver.Remove      = (*CtFile)(nil)
	_ driver.Put         = (*CtFile)(nil)
)
