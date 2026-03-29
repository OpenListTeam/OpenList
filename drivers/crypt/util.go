package crypt

import (
	stdpath "path"
	"path/filepath"
	"strings"
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

func (d *Crypt) encryptPath(path string, isFolder bool) string {
	if isFolder {
		return d.cipher.EncryptDirName(path)
	}
	dir, fileName := filepath.Split(path)
	return stdpath.Join(d.cipher.EncryptDirName(dir), d.cipher.EncryptFileName(fileName))
}
