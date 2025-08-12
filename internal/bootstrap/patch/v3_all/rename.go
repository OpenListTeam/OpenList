package v3_all

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// Rename Alist V3 driver to OpenList
func RenameAlistV3Driver() {
	storages, _, err := db.GetStorages(1, -1)
	if err != nil {
		utils.Log.Errorf("[RenameAlistV3Driver] failed to get storages: %s", err.Error())
		return
	}

	updatedCount := 0
	for _, s := range storages {
		if s.Driver == "AList V3" {
			utils.Log.Warnf("[RenameAlistV3Driver] rename storage [%d]%s from Alist V3 to OpenList", s.ID, s.MountPath)
			s.Driver = "OpenList"
			err = db.UpdateStorage(&s)
			if err != nil {
				utils.Log.Errorf("[RenameAlistV3Driver] failed to update storage [%d]%s: %s", s.ID, s.MountPath, err.Error())
			} else {
				updatedCount++
			}
		}
	}

	if updatedCount > 0 {
		utils.Log.Infof("[RenameAlistV3Driver] updated %d storages from Alist V3 to OpenList", updatedCount)
	}
}
