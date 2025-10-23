package crypt2

import (
	stdpath "path"
	"path/filepath"
	"strings"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
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

	remoteDir, err := d.getEncryptedDirName(dir)
	remoteFileName := ""
	if len(strings.TrimSpace(fileName)) > 0 {
		remoteFileName, err = d.getEncryptedName(fileName)
	}
	if err != nil {
		return stdpath.Join(d.RemotePath, remoteDir, "")
	}
	return stdpath.Join(d.RemotePath, remoteDir, remoteFileName)

}

// actual path is used for internal only. any link for user should come from remoteFullPath
func (d *Crypt) getActualPathForRemote(path string, isFolder bool) (string, error) {
	_, remoteActualPath, err := op.GetStorageAndActualPath(d.getPathForRemote(path, isFolder))
	return remoteActualPath, err
}

// 加密文件名（保留扩展名不变）
func (d *Crypt) getEncryptedName(filename string) (string, error) {
    ext := filepath.Ext(filename)
    base := filename[:len(filename)-len(ext)]
    encrypted := d.cipher.EncryptFileName(base)
    return encrypted + ext, nil
}

// 加密文件夹名
func (d *Crypt) getEncryptedDirName(dirName string) (string, error) {
    encrypted := d.cipher.EncryptDirName(dirName)
    return encrypted, nil
}

// 解密文件名or文件夹名（文件保留扩展名不变）
func (d *Crypt) getDecryptedName(filename string) (string, error) {
    ext := filepath.Ext(filename)
    base := filename[:len(filename)-len(ext)]
    decrypted,err := d.cipher.DecryptFileName(base)
    return decrypted + ext, err
}