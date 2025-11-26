package crypt

import (
	stdpath "path"
	"path/filepath"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// will give the best guessing based on the path
func guessPath(path string) (isFolder, secondTry bool) {
	if strings.HasSuffix(path, "/") {
		//confirmed a folder
		return true, false
	}
	lastSlash := strings.LastIndex(path, "/")
	if !strings.Contains(path[lastSlash:], ".") {
		//no dot, try folder then try file
		return true, true
	}
	return false, true
}

func (d *Crypt) convertPath(path string, isFolder bool) (remotePath string) {
	if isFolder {
		return d.cipher.EncryptDirName(path)
	}
	dir, fileName := filepath.Split(path)
	return stdpath.Join(d.cipher.EncryptDirName(dir), d.cipher.EncryptFileName(fileName))
}

// get the remote storage and actual path for the given path
func (d *Crypt) getStorageAndActualPath(path string, isFolder bool) (remoteStorage driver.Driver, remoteActualPath string, err error) {
	remoteStorage, remoteActualPath, err = op.GetStorageAndActualPath(d.RemotePath)
	if err == nil {
		remoteActualPath = stdpath.Join(remoteActualPath, d.convertPath(path, isFolder))
	}
	return
}
