package common

import (
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/dlclark/regexp2"
)

func IsStorageSignEnabled(rawPath string) bool {
	storage := op.GetBalancedStorage(rawPath)
	return storage != nil && storage.GetStorage().EnableSign
}

func CanWriteContentBypassUserPerms(meta *model.Meta, path string) bool {
	if meta == nil || !meta.Write {
		return false
	}
	return MetaCoversPath(meta.Path, path, meta.WSub)
}

func CanAccess(user *model.User, meta *model.Meta, reqPath string, password string) bool {
	// if the reqPath is in hide (only can check the nearest meta) and user can't see hides, can't access
	if meta != nil && !user.CanSeeHides() && meta.Hide != "" &&
		MetaCoversPath(meta.Path, path.Dir(reqPath), meta.HSub) { // the meta should apply to the parent of current path
		for _, hide := range strings.Split(meta.Hide, "\n") {
			re := regexp2.MustCompile(hide, regexp2.None)
			if isMatch, _ := re.MatchString(path.Base(reqPath)); isMatch {
				return false
			}
		}
	}
	// if is not guest and can access without password
	if user.CanAccessWithoutPassword() {
		return true
	}
	// if meta is nil or password is empty, can access
	if meta == nil || meta.Password == "" {
		return true
	}
	// if meta doesn't apply to sub_folder, can access
	if !MetaCoversPath(meta.Path, reqPath, meta.PSub) {
		return true
	}
	// validate password
	return meta.Password == password
}

func MetaCoversPath(metaPath, reqPath string, applyToSubFolder bool) bool {
	if utils.PathEqual(metaPath, reqPath) {
		return true
	}
	return utils.IsSubPath(metaPath, reqPath) && applyToSubFolder
}

// ShouldProxy TODO need optimize
// when should be proxy?
// 1. config.MustProxy()
// 2. storage.WebProxy
// 3. proxy_types
func ShouldProxy(storage driver.Driver, filename string) bool {
	if storage.Config().MustProxy() || storage.GetStorage().WebProxy {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.ProxyTypes], utils.Ext(filename)) {
		return true
	}
	return false
}
