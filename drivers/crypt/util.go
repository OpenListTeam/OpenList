package crypt

import (
	"context"
	stdpath "path"
	"path/filepath"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	log "github.com/sirupsen/logrus"
)

// will give the best guessing based on the path
func guessPath(path string) (isFolder, secondTry bool) {
	if strings.HasSuffix(path, "/") {
		//confirmed a folder
		return true, false
	}
	lastSlash := strings.LastIndex(path, "/")
	if strings.Index(path[lastSlash:], ".") < 0 {
		//no dot, try folder then try file
		return true, true
	}
	return false, true
}

func (d *Crypt) getPathForRemote(path string, isFolder bool) (remoteFullPath string) {
	if isFolder && !strings.HasSuffix(path, "/") {
		path = path + "/"
	}
	dir, fileName := filepath.Split(path)

	remoteDir := d.cipher.EncryptDirName(dir)
	remoteFileName := ""
	if len(strings.TrimSpace(fileName)) > 0 {
		remoteFileName = d.cipher.EncryptFileName(fileName)
	}
	return stdpath.Join(d.RemotePath, remoteDir, remoteFileName)

}

// actual path is used for internal only. any link for user should come from remoteFullPath
func (d *Crypt) getActualPathForRemote(path string, isFolder bool) (string, error) {
	_, remoteActualPath, err := op.GetStorageAndActualPath(d.getPathForRemote(path, isFolder))
	return remoteActualPath, err
}

func (d *Crypt) getEncryptedObject(ctx context.Context, path string) (model.Obj, error) {
	remoteFullPath := ""
	var remoteObj model.Obj
	var err, err2 error
	firstTryIsFolder, secondTry := guessPath(path)
	remoteFullPath = d.getPathForRemote(path, firstTryIsFolder)
	remoteObj, err = fs.Get(ctx, remoteFullPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		if errs.IsObjectNotFound(err) && secondTry {
			// try the opposite
			remoteFullPath = d.getPathForRemote(path, !firstTryIsFolder)
			remoteObj, err2 = fs.Get(ctx, remoteFullPath, &fs.GetArgs{NoLog: true})
			if err2 != nil {
				return nil, err2
			}
		} else {
			return nil, err
		}
	}
	return remoteObj, nil
}

func (d *Crypt) convertEncryptedObj(ctx context.Context, srcDir model.Obj, srcObjs []model.Obj, args model.BatchArgs) (model.Obj, model.Obj, []model.Obj, error) {

	srcEncryptedObj, err := d.getEncryptedObject(ctx, args.SrcDirActualPath)
	if err != nil {
		return nil, nil, nil, err
	}

	var dstEncryptedObj model.Obj
	if args.DstDirActualPath != "" {
		dstEncryptedObj, err = d.getEncryptedObject(ctx, args.DstDirActualPath)
		if err != nil {
			return nil, nil, nil, err
		}
	}

	path := srcDir.GetPath()

	objs, err := fs.List(ctx, d.getPathForRemote(path, true), &fs.ListArgs{NoLog: true, Refresh: false})
	if err != nil {
		return nil, nil, nil, err
	}

	nameSet := make(map[string]bool)
	for _, obj := range srcObjs {
		nameSet[obj.GetName()] = true
	}

	var encryptedObjs []model.Obj
	for _, obj := range objs {
		if obj.IsDir() {
			dirName, err1 := d.cipher.DecryptDirName(obj.GetName())
			if err1 != nil {
				log.Warnf("failed to decrypt dir name: %v", err1)
				continue
			}
			if nameSet[dirName] {
				encryptedObjs = append(encryptedObjs, obj)
			}
		} else {
			fileName, err1 := d.cipher.DecryptFileName(obj.GetName())
			if err1 != nil {
				log.Warnf("failed to decrypt file name: %v", err1)
				continue
			}
			if nameSet[fileName] {
				encryptedObjs = append(encryptedObjs, obj)
			}
		}
	}
	return srcEncryptedObj, dstEncryptedObj, encryptedObjs, nil
}
