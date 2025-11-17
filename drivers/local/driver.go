package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	stdpath "path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/times"
	log "github.com/sirupsen/logrus"
	_ "golang.org/x/image/webp"
)

type Local struct {
	model.Storage
	Addition
	mkdirPerm int32

	// directory size data
	directoryMap DirectoryMap

	// zero means no limit
	thumbConcurrency int
	thumbTokenBucket TokenBucket

	// video thumb position
	videoThumbPos             float64
	videoThumbPosIsPercentage bool
}

func (d *Local) Config() driver.Config {
	return config
}

func (d *Local) Init(ctx context.Context) error {
	if d.MkdirPerm == "" {
		d.mkdirPerm = 0o777
	} else {
		v, err := strconv.ParseUint(d.MkdirPerm, 8, 32)
		if err != nil {
			return err
		}
		d.mkdirPerm = int32(v)
	}
	if !utils.Exists(d.GetRootPath()) {
		return fmt.Errorf("root folder %s not exists", d.GetRootPath())
	}
	if !filepath.IsAbs(d.GetRootPath()) {
		abs, err := filepath.Abs(d.GetRootPath())
		if err != nil {
			return err
		}
		d.Addition.RootFolderPath = abs
	}
	if d.DirectorySize {
		d.directoryMap.root = d.GetRootPath()
		_, err := d.directoryMap.CalculateDirSize(d.GetRootPath())
		if err != nil {
			return err
		}
	} else {
		d.directoryMap.Clear()
	}
	if d.ThumbCacheFolder != "" && !utils.Exists(d.ThumbCacheFolder) {
		err := os.MkdirAll(d.ThumbCacheFolder, os.FileMode(d.mkdirPerm))
		if err != nil {
			return err
		}
	}
	if d.ThumbConcurrency != "" {
		v, err := strconv.ParseUint(d.ThumbConcurrency, 10, 32)
		if err != nil {
			return err
		}
		d.thumbConcurrency = int(v)
	}
	if d.thumbConcurrency == 0 {
		d.thumbTokenBucket = NewNopTokenBucket()
	} else {
		d.thumbTokenBucket = NewStaticTokenBucketWithMigration(d.thumbTokenBucket, d.thumbConcurrency)
	}
	// Check the VideoThumbPos value
	if d.VideoThumbPos == "" {
		d.VideoThumbPos = "20%"
	}
	if strings.HasSuffix(d.VideoThumbPos, "%") {
		percentage := strings.TrimSuffix(d.VideoThumbPos, "%")
		val, err := strconv.ParseFloat(percentage, 64)
		if err != nil {
			return fmt.Errorf("invalid video_thumb_pos value: %s, err: %s", d.VideoThumbPos, err)
		}
		if val < 0 || val > 100 {
			return fmt.Errorf("invalid video_thumb_pos value: %s, the precentage must be a number between 0 and 100", d.VideoThumbPos)
		}
		d.videoThumbPosIsPercentage = true
		d.videoThumbPos = val / 100
	} else {
		val, err := strconv.ParseFloat(d.VideoThumbPos, 64)
		if err != nil {
			return fmt.Errorf("invalid video_thumb_pos value: %s, err: %s", d.VideoThumbPos, err)
		}
		if val < 0 {
			return fmt.Errorf("invalid video_thumb_pos value: %s, the time must be a positive number", d.VideoThumbPos)
		}
		d.videoThumbPosIsPercentage = false
		d.videoThumbPos = val
	}
	return nil
}

func (d *Local) Drop(ctx context.Context) error {
	return nil
}

func (d *Local) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Local) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	fullPath := dir.GetPath()
	rawFiles, err := readDir(fullPath)
	if d.DirectorySize && args.Refresh {
		d.directoryMap.RecalculateDirSize()
	}
	if err != nil {
		return nil, err
	}
	var files []model.Obj
	for _, f := range rawFiles {
		if d.ShowHidden || !isHidden(f, fullPath) {
			files = append(files, d.FileInfoToObj(ctx, f, args.ReqPath, fullPath))
		}
	}
	return files, nil
}

func (d *Local) FileInfoToObj(ctx context.Context, f fs.FileInfo, reqPath string, fullPath string) model.Obj {
	thumb := ""
	if d.Thumbnail {
		typeName := utils.GetFileType(f.Name())
		if typeName == conf.IMAGE || typeName == conf.VIDEO {
			thumb = common.GetApiUrl(ctx) + stdpath.Join("/d", reqPath, f.Name())
			thumb = utils.EncodePath(thumb, true)
			thumb += "?type=thumb&sign=" + sign.Sign(stdpath.Join(reqPath, f.Name()))
		}
	}
	isFolder := f.IsDir() || isSymlinkDir(f, fullPath)
	var size int64
	if isFolder {
		node, ok := d.directoryMap.Get(filepath.Join(fullPath, f.Name()))
		if ok {
			size = node.fileSum + node.directorySum
		}
	} else {
		size = f.Size()
	}
	var ctime time.Time
	t, err := times.Stat(stdpath.Join(fullPath, f.Name()))
	if err == nil {
		if t.HasBirthTime() {
			ctime = t.BirthTime()
		}
	}

	file := model.ObjThumb{
		Object: model.Object{
			Path:     filepath.Join(fullPath, f.Name()),
			Name:     f.Name(),
			Modified: f.ModTime(),
			Size:     size,
			IsFolder: isFolder,
			Ctime:    ctime,
		},
		Thumbnail: model.Thumbnail{
			Thumbnail: thumb,
		},
	}
	return &file
}

func (d *Local) Get(ctx context.Context, path string) (model.Obj, error) {
	path = filepath.Join(d.GetRootPath(), path)
	f, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errs.ObjectNotFound
		}
		return nil, err
	}
	isFolder := f.IsDir() || isSymlinkDir(f, path)
	size := f.Size()
	if isFolder {
		node, ok := d.directoryMap.Get(path)
		if ok {
			size = node.fileSum + node.directorySum
		}
	} else {
		size = f.Size()
	}
	var ctime time.Time
	t, err := times.Stat(path)
	if err == nil {
		if t.HasBirthTime() {
			ctime = t.BirthTime()
		}
	}
	file := model.Object{
		Path:     path,
		Name:     f.Name(),
		Modified: f.ModTime(),
		Ctime:    ctime,
		Size:     size,
		IsFolder: isFolder,
	}
	return &file, nil
}

func (d *Local) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	fullPath := file.GetPath()
	link := &model.Link{}
	var MFile model.File
	if args.Type == "thumb" && utils.Ext(file.GetName()) != "svg" {
		var buf *bytes.Buffer
		var thumbPath *string
		err := d.thumbTokenBucket.Do(ctx, func() error {
			var err error
			buf, thumbPath, err = d.getThumb(file)
			return err
		})
		if err != nil {
			return nil, err
		}
		link.Header = http.Header{
			"Content-Type": []string{"image/png"},
		}
		if thumbPath != nil {
			open, err := os.Open(*thumbPath)
			if err != nil {
				return nil, err
			}
			// Get thumbnail file size for Content-Length
			stat, err := open.Stat()
			if err != nil {
				open.Close()
				return nil, err
			}
			link.ContentLength = int64(stat.Size())
			MFile = open
		} else {
			MFile = bytes.NewReader(buf.Bytes())
			link.ContentLength = int64(buf.Len())
		}
	} else {
		open, err := os.Open(fullPath)
		if err != nil {
			return nil, err
		}
		link.ContentLength = file.GetSize()
		MFile = open
	}
	link.SyncClosers.AddIfCloser(MFile)
	link.RangeReader = stream.GetRangeReaderFromMFile(link.ContentLength, MFile)
	link.RequireReference = link.SyncClosers.Length() > 0
	return link, nil
}

func (d *Local) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	fullPath := filepath.Join(parentDir.GetPath(), dirName)
	err := os.MkdirAll(fullPath, os.FileMode(d.mkdirPerm))
	if err != nil {
		return err
	}
	return nil
}

func (d *Local) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcPath := srcObj.GetPath()
	dstPath := filepath.Join(dstDir.GetPath(), srcObj.GetName())
	if utils.IsSubPath(srcPath, dstPath) {
		return fmt.Errorf("the destination folder is a subfolder of the source folder")
	}
	err := os.Rename(srcPath, dstPath)
	if isCrossDeviceError(err) {
		// 跨设备移动，变更为移动任务
		return errs.NotImplement
	}
	if err == nil {
		srcParent := filepath.Dir(srcPath)
		dstParent := filepath.Dir(dstPath)
		if d.directoryMap.Has(srcParent) {
			d.directoryMap.UpdateDirSize(srcParent)
			d.directoryMap.UpdateDirParents(srcParent)
		}
		if d.directoryMap.Has(dstParent) {
			d.directoryMap.UpdateDirSize(dstParent)
			d.directoryMap.UpdateDirParents(dstParent)
		}
	}
	return err
}

func (d *Local) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	srcPath := srcObj.GetPath()
	dstPath := filepath.Join(filepath.Dir(srcPath), newName)
	err := os.Rename(srcPath, dstPath)
	if err != nil {
		return err
	}

	if srcObj.IsDir() {
		if d.directoryMap.Has(srcPath) {
			d.directoryMap.DeleteDirNode(srcPath)
			d.directoryMap.CalculateDirSize(dstPath)
		}
	}

	return nil
}

func (d *Local) Copy(_ context.Context, srcObj, dstDir model.Obj) error {
	srcPath := srcObj.GetPath()
	dstPath := filepath.Join(dstDir.GetPath(), srcObj.GetName())
	if utils.IsSubPath(srcPath, dstPath) {
		return fmt.Errorf("the destination folder is a subfolder of the source folder")
	}
	info, err := os.Lstat(srcPath)
	if err != nil {
		return err
	}
	// 复制regular文件会返回errs.NotImplement, 转为复制任务
	if err = d.tryCopy(srcPath, dstPath, info); err != nil {
		return err
	}

	if d.directoryMap.Has(filepath.Dir(dstPath)) {
		d.directoryMap.UpdateDirSize(filepath.Dir(dstPath))
		d.directoryMap.UpdateDirParents(filepath.Dir(dstPath))
	}

	return nil
}

func (d *Local) Remove(ctx context.Context, obj model.Obj) error {
	var err error
	if utils.SliceContains([]string{"", "delete permanently"}, d.RecycleBinPath) {
		if obj.IsDir() {
			err = os.RemoveAll(obj.GetPath())
		} else {
			err = os.Remove(obj.GetPath())
		}
	} else {
		objPath := obj.GetPath()
		objName := obj.GetName()
		var relPath string
		relPath, err = filepath.Rel(d.GetRootPath(), filepath.Dir(objPath))
		if err != nil {
			return err
		}
		recycleBinPath := filepath.Join(d.RecycleBinPath, relPath)
		if !utils.Exists(recycleBinPath) {
			err = os.MkdirAll(recycleBinPath, 0o755)
			if err != nil {
				return err
			}
		}

		dstPath := filepath.Join(recycleBinPath, objName)
		if utils.Exists(dstPath) {
			dstPath = filepath.Join(recycleBinPath, objName+"_"+time.Now().Format("20060102150405"))
		}
		err = os.Rename(objPath, dstPath)
	}
	if err != nil {
		return err
	}
	if obj.IsDir() {
		if d.directoryMap.Has(obj.GetPath()) {
			d.directoryMap.DeleteDirNode(obj.GetPath())
			d.directoryMap.UpdateDirSize(filepath.Dir(obj.GetPath()))
			d.directoryMap.UpdateDirParents(filepath.Dir(obj.GetPath()))
		}
	} else {
		if d.directoryMap.Has(filepath.Dir(obj.GetPath())) {
			d.directoryMap.UpdateDirSize(filepath.Dir(obj.GetPath()))
			d.directoryMap.UpdateDirParents(filepath.Dir(obj.GetPath()))
		}
	}

	return nil
}

func (d *Local) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	fullPath := filepath.Join(dstDir.GetPath(), stream.GetName())
	out, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = out.Close()
		if errors.Is(err, context.Canceled) {
			_ = os.Remove(fullPath)
		}
	}()
	err = utils.CopyWithCtx(ctx, out, stream, stream.GetSize(), up)
	if err != nil {
		return err
	}
	err = os.Chtimes(fullPath, stream.ModTime(), stream.ModTime())
	if err != nil {
		log.Errorf("[local] failed to change time of %s: %s", fullPath, err)
	}
	if d.directoryMap.Has(dstDir.GetPath()) {
		d.directoryMap.UpdateDirSize(dstDir.GetPath())
		d.directoryMap.UpdateDirParents(dstDir.GetPath())
	}

	return nil
}

func (d *Local) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	du, err := getDiskUsage(d.RootFolderPath)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: du,
	}, nil
}

var _ driver.Driver = (*Local)(nil)
