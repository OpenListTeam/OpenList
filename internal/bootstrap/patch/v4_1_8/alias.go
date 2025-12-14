package v4_1_8

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// FixAliasConfig upgrade the old version of the Addition of the Alias driver
func FixAliasConfig() {
	storages, _, err := db.GetStorages(1, -1)
	if err != nil {
		utils.Log.Errorf("[FixAliasConfig] failed to get storages: %s", err.Error())
		return
	}
	for _, s := range storages {
		if s.Driver != "Alias" {
			continue
		}
		addition := make(map[string]any)
		err = utils.Json.UnmarshalFromString(s.Addition, &addition)
		if err != nil {
			utils.Log.Errorf("[FixAliasConfig] failed to unmarshal addition of [%d]%s: %s", s.ID, s.MountPath, err.Error())
			continue
		}
		if _, ok := addition["read_conflict_policy"]; ok {
			utils.Log.Infof("[FixAliasConfig] skip fixing [%d]%s because the addition already has \"read_conflict_policy\" key", s.ID, s.MountPath)
			continue
		}
		var protectSameName, parallelWrite, writable bool
		protectSameNameAny, ok := addition["protect_same_name"]
		if ok {
			delete(addition, "protect_same_name")
			protectSameName, ok = protectSameNameAny.(bool)
		}
		if !ok {
			protectSameName = false
		}
		parallelWriteAny, ok := addition["parallel_write"]
		if ok {
			delete(addition, "parallel_write")
			parallelWrite, ok = parallelWriteAny.(bool)
		}
		if !ok {
			parallelWrite = false
		}
		writableAny, ok := addition["writable"]
		if ok {
			delete(addition, "writable")
			writable, ok = writableAny.(bool)
		}
		if !ok {
			writable = false
		}
		if !writable {
			addition["write_conflict_policy"] = "disabled"
			addition["put_conflict_policy"] = "disabled"
		} else if !protectSameName && !parallelWrite {
			addition["write_conflict_policy"] = "first"
			addition["put_conflict_policy"] = "first"
		} else if protectSameName && !parallelWrite {
			addition["write_conflict_policy"] = "deterministic"
			addition["put_conflict_policy"] = "deterministic"
		} else if !protectSameName && parallelWrite {
			addition["write_conflict_policy"] = "all"
			addition["put_conflict_policy"] = "all"
		} else {
			addition["write_conflict_policy"] = "deterministic_or_all"
			addition["put_conflict_policy"] = "deterministic_or_all"
		}
		addition["read_conflict_policy"] = "first"
		s.Addition, err = utils.Json.MarshalToString(addition)
		if err != nil {
			utils.Log.Errorf("[FixAliasConfig] failed to marshal addition of [%d]%s: %s", s.ID, s.MountPath, err.Error())
			continue
		}
		err = db.UpdateStorage(&s)
		if err != nil {
			utils.Log.Errorf("[FixAliasConfig] failed to update storage [%d]%s: %s", s.ID, s.MountPath, err.Error())
		}
	}
}
