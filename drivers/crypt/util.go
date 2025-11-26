package crypt

import (
	stdpath "path"
	"path/filepath"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// will give the best guessing based on the path
func guessPath(path string) (isFolder, secondTry bool) {
	if strings.HasSuffix(path, "/") {
		// confirmed a folder
		return true, false
	}
	lastSlash := strings.LastIndex(path, "/")
	if strings.Index(path[lastSlash:], ".") < 0 {
		// no dot, try folder then try file
		return true, true
	}
	return false, true
}

func (d *Crypt) getActualPathForRemote(path string, isFolder bool) string {
	if path == "" || path == "/" {
		return ""
	}
	if isFolder && !strings.HasSuffix(path, "/") {
		path = path + "/"
	}
	dir, fileName := filepath.Split(path)

	remoteDir := d.cipher.EncryptDirName(dir)
	remoteFileName := ""
	if len(strings.TrimSpace(fileName)) > 0 {
		remoteFileName = d.cipher.EncryptFileName(fileName)
	}
	return stdpath.Join(remoteDir, remoteFileName)
}

// actual path is used for internal only. any link for user should come from remoteFullPath
func (d *Crypt) getStorageAndActualPathForRemote(path string, isFolder bool) (driver.Driver, string, error) {
	storage, rootActualPath, restActualPath, err := d.getStorageAndPlainPath(path)
	if err != nil {
		return nil, "", err
	}
	encryptedSubPath := d.getActualPathForRemote(restActualPath, isFolder)
	return storage, stdpath.Join(rootActualPath, encryptedSubPath), nil
}

func (d *Crypt) getStorageAndPlainPath(path string) (storage driver.Driver, rootActualPath string, restActualPath string, err error) {
	storage, actualPath, err := op.GetStorageAndActualPath(stdpath.Join(d.RemotePath, path))
	if err != nil {
		return nil, "", "", err
	}
	subRoot := ""
	if sr, ok := strings.CutPrefix(d.RemotePath, utils.GetActualMountPath(storage.GetStorage().MountPath)); ok {
		subRoot = sr
	}
	subPath := strings.TrimPrefix(actualPath, subRoot)
	return storage, subRoot, subPath, nil
}
