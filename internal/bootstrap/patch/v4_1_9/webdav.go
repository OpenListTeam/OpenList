package v4_1_9

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// EnableWebDavProxy updates Webdav driver storages to enable proxy
func EnableWebDavProxy() {
	storages, _, err := db.GetStorages(1, -1)
	if err != nil {
		utils.Log.Errorf("[EnableWebDavProxy] failed to get storages: %s", err.Error())
		return
	}
	for _, s := range storages {
		if s.Driver != "WebDav" {
			continue
		}
		if !s.WebProxy {
			s.WebProxy = true
		}
		if s.WebdavPolicy == "302_redirect" {
			s.WebdavPolicy = "native_proxy"
		}
		err = db.UpdateStorage(&s)
		if err != nil {
			utils.Log.Errorf("[EnableWebDavProxy] failed to update storage [%d]%s: %s", s.ID, s.MountPath, err.Error())
		}
	}
}
