package crypt

import (
	"bytes"
	"context"
	"fmt"
	"io"
	stdpath "path"
	"regexp"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	rcCrypt "github.com/rclone/rclone/backend/crypt"
	"github.com/rclone/rclone/fs/config/configmap"
	"github.com/rclone/rclone/fs/config/obscure"
	log "github.com/sirupsen/logrus"
)

type Crypt struct {
	model.Storage
	Addition
	cipher        *rcCrypt.Cipher
	remoteStorage driver.Driver
}

const obfuscatedPrefix = "___Obfuscated___"

func (d *Crypt) Config() driver.Config {
	return config
}

func (d *Crypt) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Crypt) Init(ctx context.Context) error {
	//obfuscate credentials if it's updated or just created
	err := d.updateObfusParm(&d.Password)
	if err != nil {
		return fmt.Errorf("failed to obfuscate password: %w", err)
	}
	err = d.updateObfusParm(&d.Salt)
	if err != nil {
		return fmt.Errorf("failed to obfuscate salt: %w", err)
	}

	isCryptExt := regexp.MustCompile(`^[.][A-Za-z0-9-_]{2,}$`).MatchString
	if !isCryptExt(d.EncryptedSuffix) {
		return fmt.Errorf("EncryptedSuffix is Illegal")
	}
	d.FileNameEncoding = utils.GetNoneEmpty(d.FileNameEncoding, "base64")
	d.EncryptedSuffix = utils.GetNoneEmpty(d.EncryptedSuffix, ".bin")

	op.MustSaveDriverStorage(d)

	//need remote storage exist
	storage, err := fs.GetStorage(d.RemotePath, &fs.GetStoragesArgs{})
	if err != nil {
		return fmt.Errorf("can't find remote storage: %w", err)
	}
	d.remoteStorage = storage

	p, _ := strings.CutPrefix(d.Password, obfuscatedPrefix)
	p2, _ := strings.CutPrefix(d.Salt, obfuscatedPrefix)
	config := configmap.Simple{
		"password":                  p,
		"password2":                 p2,
		"filename_encryption":       d.FileNameEnc,
		"directory_name_encryption": d.DirNameEnc,
		"filename_encoding":         d.FileNameEncoding,
		"suffix":                    d.EncryptedSuffix,
		"pass_bad_blocks":           "",
	}
	c, err := rcCrypt.NewCipher(config)
	if err != nil {
		return fmt.Errorf("failed to create Cipher: %w", err)
	}
	d.cipher = c

	return nil
}

func (d *Crypt) updateObfusParm(str *string) error {
	temp := *str
	if !strings.HasPrefix(temp, obfuscatedPrefix) {
		temp, err := obscure.Obscure(temp)
		if err != nil {
			return err
		}
		temp = obfuscatedPrefix + temp
		*str = temp
	}
	return nil
}

func (d *Crypt) Drop(ctx context.Context) error {
	return nil
}

func (d *Crypt) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	path := dir.GetPath()
	//return d.list(ctx, d.RemotePath, path)
	//remoteFull

	objs, err := fs.List(ctx, d.getPathForRemote(path, true), &fs.ListArgs{NoLog: true, Refresh: args.Refresh})
	// the obj must implement the model.SetPath interface
	// return objs, err
	if err != nil {
		return nil, err
	}

	var result []model.Obj
	for _, obj := range objs {
		if obj.IsDir() {
			name, err := d.cipher.DecryptDirName(obj.GetName())
			if err != nil {
				//filter illegal files
				continue
			}
			if !d.ShowHidden && strings.HasPrefix(name, ".") {
				continue
			}
			objRes := model.Object{
				Name:     name,
				Size:     0,
				Modified: obj.ModTime(),
				IsFolder: obj.IsDir(),
				Ctime:    obj.CreateTime(),
				// discarding hash as it's encrypted
			}
			result = append(result, &objRes)
		} else {
			thumb, ok := model.GetThumb(obj)
			size, err := d.cipher.DecryptedSize(obj.GetSize())
			if err != nil {
				//filter illegal files
				continue
			}
			name, err := d.cipher.DecryptFileName(obj.GetName())
			if err != nil {
				//filter illegal files
				continue
			}
			if !d.ShowHidden && strings.HasPrefix(name, ".") {
				continue
			}
			objRes := model.Object{
				Name:     name,
				Size:     size,
				Modified: obj.ModTime(),
				IsFolder: obj.IsDir(),
				Ctime:    obj.CreateTime(),
				// discarding hash as it's encrypted
			}
			if d.Thumbnail && thumb == "" {
				thumbPath := stdpath.Join(args.ReqPath, ".thumbnails", name+".webp")
				thumb = fmt.Sprintf("%s/d%s?sign=%s",
					common.GetApiUrl(ctx),
					utils.EncodePath(thumbPath, true),
					sign.Sign(thumbPath))
			}
			if !ok && !d.Thumbnail {
				result = append(result, &objRes)
			} else {
				objWithThumb := model.ObjThumb{
					Object: objRes,
					Thumbnail: model.Thumbnail{
						Thumbnail: thumb,
					},
				}
				result = append(result, &objWithThumb)
			}
		}
	}

	return result, nil
}

func (d *Crypt) Get(ctx context.Context, path string) (model.Obj, error) {
	if utils.PathEqual(path, "/") {
		return &model.Object{
			Name:     "Root",
			IsFolder: true,
			Path:     "/",
		}, nil
	}
	remoteFullPath := ""
	var remoteObj model.Obj
	var err, err2 error
	firstTryIsFolder, secondTry := guessPath(path)
	remoteFullPath = d.getPathForRemote(path, firstTryIsFolder)
	remoteObj, err = fs.Get(ctx, remoteFullPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		if errs.IsObjectNotFound(err) && secondTry {
			//try the opposite
			remoteFullPath = d.getPathForRemote(path, !firstTryIsFolder)
			remoteObj, err2 = fs.Get(ctx, remoteFullPath, &fs.GetArgs{NoLog: true})
			if err2 != nil {
				return nil, err2
			}
		} else {
			return nil, err
		}
	}
	var size int64 = 0
	name := ""
	if !remoteObj.IsDir() {
		size, err = d.cipher.DecryptedSize(remoteObj.GetSize())
		if err != nil {
			log.Warnf("DecryptedSize failed for %s ,will use original size, err:%s", path, err)
			size = remoteObj.GetSize()
		}
		name, err = d.cipher.DecryptFileName(remoteObj.GetName())
		if err != nil {
			log.Warnf("DecryptFileName failed for %s ,will use original name, err:%s", path, err)
			name = remoteObj.GetName()
		}
	} else {
		name, err = d.cipher.DecryptDirName(remoteObj.GetName())
		if err != nil {
			log.Warnf("DecryptDirName failed for %s ,will use original name, err:%s", path, err)
			name = remoteObj.GetName()
		}
	}
	obj := &model.Object{
		Path:     path,
		Name:     name,
		Size:     size,
		Modified: remoteObj.ModTime(),
		IsFolder: remoteObj.IsDir(),
	}
	return obj, nil
	//return nil, errs.ObjectNotFound
}

// https://github.com/rclone/rclone/blob/v1.67.0/backend/crypt/cipher.go#L37
const fileHeaderSize = 32

func (d *Crypt) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	dstDirActualPath, err := d.getActualPathForRemote(file.GetPath(), false)
	if err != nil {
		return nil, fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	remoteLink, remoteFile, err := op.Link(ctx, d.remoteStorage, dstDirActualPath, args)
	if err != nil {
		return nil, err
	}

	remoteSize := remoteLink.ContentLength
	if remoteSize <= 0 {
		remoteSize = remoteFile.GetSize()
	}
	rrf, err := stream.GetRangeReaderFromLink(remoteSize, remoteLink)
	if err != nil {
		_ = remoteLink.Close()
		return nil, fmt.Errorf("the remote storage driver need to be enhanced to support encrytion")
	}

	mu := &sync.Mutex{}
	var fileHeader []byte
	rangeReaderFunc := func(ctx context.Context, offset, limit int64) (io.ReadCloser, error) {
		length := limit
		if offset == 0 && limit > 0 {
			mu.Lock()
			if limit <= fileHeaderSize {
				defer mu.Unlock()
				if fileHeader != nil {
					return io.NopCloser(bytes.NewReader(fileHeader[:limit])), nil
				}
				length = fileHeaderSize
			} else if fileHeader == nil {
				defer mu.Unlock()
			} else {
				mu.Unlock()
			}
		}

		remoteReader, err := rrf.RangeRead(ctx, http_range.Range{Start: offset, Length: length})
		if err != nil {
			return nil, err
		}

		if offset == 0 && limit > 0 {
			fileHeader = make([]byte, fileHeaderSize)
			n, err := io.ReadFull(remoteReader, fileHeader)
			if n != fileHeaderSize {
				fileHeader = nil
				return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", fileHeaderSize, n, err)
			}
			if limit <= fileHeaderSize {
				remoteReader.Close()
				return io.NopCloser(bytes.NewReader(fileHeader[:limit])), nil
			} else {
				remoteReader = utils.ReadCloser{
					Reader: io.MultiReader(bytes.NewReader(fileHeader), remoteReader),
					Closer: remoteReader,
				}
			}
		}
		return remoteReader, nil
	}
	return &model.Link{
		RangeReader: stream.RangeReaderFunc(func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
			readSeeker, err := d.cipher.DecryptDataSeek(ctx, rangeReaderFunc, httpRange.Start, httpRange.Length)
			if err != nil {
				return nil, err
			}
			return readSeeker, nil
		}),
		SyncClosers: utils.NewSyncClosers(remoteLink),
	}, nil
}

func (d *Crypt) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	dstDirActualPath, err := d.getActualPathForRemote(parentDir.GetPath(), true)
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	dir := d.cipher.EncryptDirName(dirName)
	return op.MakeDir(ctx, d.remoteStorage, stdpath.Join(dstDirActualPath, dir))
}

func (d *Crypt) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcRemoteActualPath, err := d.getActualPathForRemote(srcObj.GetPath(), srcObj.IsDir())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	dstRemoteActualPath, err := d.getActualPathForRemote(dstDir.GetPath(), dstDir.IsDir())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	return op.Move(ctx, d.remoteStorage, srcRemoteActualPath, dstRemoteActualPath)
}

func (d *Crypt) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	remoteActualPath, err := d.getActualPathForRemote(srcObj.GetPath(), srcObj.IsDir())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	var newEncryptedName string
	if srcObj.IsDir() {
		newEncryptedName = d.cipher.EncryptDirName(newName)
	} else {
		newEncryptedName = d.cipher.EncryptFileName(newName)
	}
	return op.Rename(ctx, d.remoteStorage, remoteActualPath, newEncryptedName)
}

func (d *Crypt) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	srcRemoteActualPath, err := d.getActualPathForRemote(srcObj.GetPath(), srcObj.IsDir())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	dstRemoteActualPath, err := d.getActualPathForRemote(dstDir.GetPath(), dstDir.IsDir())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	return op.Copy(ctx, d.remoteStorage, srcRemoteActualPath, dstRemoteActualPath)

}

func (d *Crypt) Remove(ctx context.Context, obj model.Obj) error {
	remoteActualPath, err := d.getActualPathForRemote(obj.GetPath(), obj.IsDir())
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}
	return op.Remove(ctx, d.remoteStorage, remoteActualPath)
}

func (d *Crypt) Put(ctx context.Context, dstDir model.Obj, streamer model.FileStreamer, up driver.UpdateProgress) error {
	dstDirActualPath, err := d.getActualPathForRemote(dstDir.GetPath(), true)
	if err != nil {
		return fmt.Errorf("failed to convert path to remote path: %w", err)
	}

	// Encrypt the data into wrappedIn
	wrappedIn, err := d.cipher.EncryptData(streamer)
	if err != nil {
		return fmt.Errorf("failed to EncryptData: %w", err)
	}

	// doesn't support seekableStream, since rapid-upload is not working for encrypted data
	streamOut := &stream.FileStream{
		Obj: &model.Object{
			ID:       streamer.GetID(),
			Path:     streamer.GetPath(),
			Name:     d.cipher.EncryptFileName(streamer.GetName()),
			Size:     d.cipher.EncryptedSize(streamer.GetSize()),
			Modified: streamer.ModTime(),
			IsFolder: streamer.IsDir(),
		},
		Reader:            wrappedIn,
		Mimetype:          "application/octet-stream",
		ForceStreamUpload: true,
		Exist:             streamer.GetExist(),
	}
	err = op.Put(ctx, d.remoteStorage, dstDirActualPath, streamOut, up, false)
	if err != nil {
		return err
	}
	return nil
}

//func (d *Safe) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*Crypt)(nil)
