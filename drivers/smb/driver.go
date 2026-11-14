package smb

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/cloudsoda/go-smb2"
)

type SMB struct {
	lastConnTime atomic.Int64
	publishMode  atomic.Int32 // see publishModeFor
	probeMu      sync.Mutex
	staging      driver.StagingInUse
	inPlace      driver.KeyedMutex // uploads of a file written in place take turns
	model.Storage
	Addition
	fs *smb2.Share
}

func (d *SMB) Config() driver.Config {
	return config
}

func (d *SMB) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *SMB) Init(ctx context.Context) error {
	if !strings.Contains(d.Addition.Address, ":") {
		d.Addition.Address = d.Addition.Address + ":445"
	}
	d.publishMode.Store(publishUnknown)
	return d._initFS(ctx)
}

func (d *SMB) Drop(ctx context.Context) error {
	if d.fs != nil {
		_ = d.fs.Umount()
	}
	return nil
}

func (d *SMB) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if err := d.checkConn(ctx); err != nil {
		return nil, err
	}
	fullPath := dir.GetPath()
	rawFiles, err := d.fs.ReadDir(fullPath)
	if err != nil {
		d.cleanLastConnTime()
		return nil, err
	}
	d.updateLastConnTime()
	files := make([]model.Obj, 0, len(rawFiles))
	for _, f := range rawFiles {
		if !f.IsDir() {
			if created, ok := driver.StagingCreated(d.GetStorage(), f.Name()); ok {
				// an upload in progress; reclaim the ones a killed process left
				p := path.Join(fullPath, f.Name())
				if d.staging.Reclaimable(p, created, f.ModTime()) {
					_ = d.fs.Remove(p)
				}
				continue
			}
		}
		file := model.Object{
			Path:     path.Join(fullPath, f.Name()),
			Name:     f.Name(),
			Modified: f.ModTime(),
			Size:     f.Size(),
			IsFolder: f.IsDir(),
			Ctime:    f.(*smb2.FileStat).CreationTime,
		}

		files = append(files, &file)
	}
	return files, nil
}

func (d *SMB) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if err := d.checkConn(ctx); err != nil {
		return nil, err
	}
	fullPath := file.GetPath()
	remoteFile, err := d.fs.Open(fullPath)
	if err != nil {
		d.cleanLastConnTime()
		return nil, err
	}
	d.updateLastConnTime()
	mFile := &stream.RateLimitFile{
		File:    remoteFile,
		Limiter: stream.ServerDownloadLimit,
		Ctx:     ctx,
	}
	return &model.Link{
		RangeReader:      stream.GetRangeReaderFromMFile(file.GetSize(), mFile),
		SyncClosers:      utils.NewSyncClosers(remoteFile),
		RequireReference: true,
	}, nil
}

func (d *SMB) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	if err := d.checkConn(ctx); err != nil {
		return err
	}
	fullPath := filepath.Join(parentDir.GetPath(), dirName)
	err := d.fs.MkdirAll(fullPath, 0700)
	if err != nil {
		d.cleanLastConnTime()
		return err
	}
	d.updateLastConnTime()
	return nil
}

func (d *SMB) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	if err := d.checkConn(ctx); err != nil {
		return err
	}
	srcPath := srcObj.GetPath()
	dstPath := filepath.Join(dstDir.GetPath(), srcObj.GetName())
	err := d.fs.Rename(srcPath, dstPath)
	if err != nil {
		d.cleanLastConnTime()
		return err
	}
	d.updateLastConnTime()
	return nil
}

func (d *SMB) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if err := d.checkConn(ctx); err != nil {
		return err
	}
	srcPath := srcObj.GetPath()
	dstPath := filepath.Join(filepath.Dir(srcPath), newName)
	err := d.fs.Rename(srcPath, dstPath)
	if err != nil {
		d.cleanLastConnTime()
		return err
	}
	d.updateLastConnTime()
	return nil
}

func (d *SMB) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	if err := d.checkConn(ctx); err != nil {
		return err
	}
	srcPath := srcObj.GetPath()
	dstPath := filepath.Join(dstDir.GetPath(), srcObj.GetName())
	var err error
	if srcObj.IsDir() {
		err = d.CopyDir(srcPath, dstPath)
	} else {
		err = d.CopyFile(srcPath, dstPath)
	}
	if err != nil {
		d.cleanLastConnTime()
		return err
	}
	d.updateLastConnTime()
	return nil
}

func (d *SMB) Remove(ctx context.Context, obj model.Obj) error {
	if err := d.checkConn(ctx); err != nil {
		return err
	}
	var err error
	fullPath := obj.GetPath()
	if obj.IsDir() {
		err = d.fs.RemoveAll(fullPath)
	} else {
		err = d.fs.Remove(fullPath)
	}
	if err != nil {
		d.cleanLastConnTime()
		return err
	}
	d.updateLastConnTime()
	return nil
}

func (d *SMB) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	if err := d.checkConn(ctx); err != nil {
		return err
	}
	dir := dstDir.GetPath()
	fullPath := filepath.Join(dir, stream.GetName())
	mode := d.publishModeFor(dir)
	target, flag := fullPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC
	switch mode {
	case publishStaged:
		name := driver.StagingName(d.GetStorage())
		target, flag = filepath.Join(dir, name), os.O_RDWR|os.O_CREATE|os.O_EXCL
		// released after the staging file is renamed or removed
		release := d.staging.Hold(path.Join(dir, name))
		defer release()
	case publishInPlace:
		// in turn with other uploads of the file, so that what a failed one
		// removes is all its own
		unlock, err := d.inPlace.Lock(ctx, fullPath)
		if err != nil {
			return err
		}
		defer unlock()
	case publishNewOnly:
		flag = os.O_RDWR | os.O_CREATE | os.O_EXCL
	}
	out, err := d.fs.OpenFile(target, flag, 0o666)
	if err != nil {
		d.cleanLastConnTime()
		if mode == publishNewOnly {
			if _, serr := d.fs.Stat(fullPath); serr == nil {
				return errCannotReplace
			}
		}
		return err
	}
	d.updateLastConnTime()
	defer func() {
		_ = out.Close()
		// remove what this upload wrote: its staging file, never a destination
		// another upload may own by now, or the file it created, or cut short
		// to write in place in its turn, which must not pass for the file
		if err != nil {
			_ = d.fs.Remove(target)
		}
	}()
	err = utils.CopyWithCtx(ctx, out, driver.NewLimitedUploadStream(ctx, stream), stream.GetSize(), up)
	if err != nil || target == fullPath {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	err = d.fs.Rename(target, fullPath)
	return err
}

func (d *SMB) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	if err := d.checkConn(ctx); err != nil {
		return nil, err
	}
	stat, err := d.fs.Statfs(d.RootFolderPath)
	if err != nil {
		return nil, err
	}
	total := int64(stat.BlockSize() * stat.TotalBlockCount())
	free := int64(stat.BlockSize() * stat.AvailableBlockCount())
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  total - free,
		},
	}, nil
}

//func (d *SMB) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*SMB)(nil)
