package op

import (
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

// GetStorageAndActualPath Get the corresponding storage and actual path
// for path: remove the mount path prefix and join the actual root folder if exists
func GetStorageAndActualPath(rawPath string) (storage driver.Driver, actualPath string, err error) {
	rawPath = utils.FixAndCleanPath(rawPath)
	storage = GetBalancedStorage(rawPath)
	if storage == nil {
		if rawPath == "/" {
			err = errs.NewErr(errs.StorageNotFound, "please add a storage first")
			return
		}
		err = errs.NewErr(errs.StorageNotFound, "rawPath: %s", rawPath)
		return
	}
	log.Debugln("use storage: ", storage.GetStorage().MountPath)
	mountPath := utils.GetActualMountPath(storage.GetStorage().MountPath)
	actualPath = utils.FixAndCleanPath(strings.TrimPrefix(rawPath, mountPath))
	return
}

// GetStorageAndActualPathByMountPath resolves rawPath against a specific
// storage mount. This is needed for balance mounts, where resolving the same
// virtual path repeatedly may intentionally select different backends.
func GetStorageAndActualPathByMountPath(rawPath, mountPath string) (storage driver.Driver, actualPath string, err error) {
	rawPath = utils.FixAndCleanPath(rawPath)
	mountPath = utils.FixAndCleanPath(mountPath)
	storage, err = GetStorageByMountPath(mountPath)
	if err != nil {
		return nil, "", err
	}

	actualMountPath := utils.GetActualMountPath(storage.GetStorage().MountPath)
	if !utils.IsSubPath(actualMountPath, rawPath) {
		return nil, "", errs.NewErr(errs.StorageNotFound, "rawPath %q is outside storage mount %q", rawPath, mountPath)
	}
	actualPath = strings.TrimPrefix(rawPath, actualMountPath)
	if actualPath == "" {
		actualPath = "/"
	}
	return storage, utils.FixAndCleanPath(actualPath), nil
}

// urlTreeSplitLineFormPath 分割path中分割真实路径和UrlTree定义字符串
func urlTreeSplitLineFormPath(path string) (pp string, file string) {
	// url.PathUnescape 会移除 // ，手动加回去
	path = strings.Replace(path, "https:/", "https://", 1)
	path = strings.Replace(path, "http:/", "http://", 1)
	if strings.Contains(path, ":https:/") || strings.Contains(path, ":http:/") {
		// URL-Tree模式 /url_tree_drivr/file_name[:size[:time]]:https://example.com/file
		fPath := strings.SplitN(path, ":", 2)[0]
		pp, _ = stdpath.Split(fPath)
		file = path[len(pp):]
	} else if strings.Contains(path, "/https:/") || strings.Contains(path, "/http:/") {
		// URL-Tree模式 /url_tree_drivr/https://example.com/file
		index := strings.Index(path, "/http://")
		if index == -1 {
			index = strings.Index(path, "/https://")
		}
		pp = path[:index]
		file = path[index+1:]
	} else {
		pp, file = stdpath.Split(path)
	}
	if pp == "" {
		pp = "/"
	}
	return
}
