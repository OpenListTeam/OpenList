package v3_24_0

import (
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// HashPwdForOldVersion encode passwords using SHA256
// First published: 75acbcc perf: sha256 for user's password (close #3552) by Andy Hsu
func HashPwdForOldVersion() {
	users, _, err := op.GetUsers(1, -1)
	if err != nil {
		utils.Log.Errorf("[hash pwd for old version] failed get users: %v", err)
		return
	}
	for i := range users {
		user := users[i]
		if user.PwdHash == "" {
			user.SetPassword(user.Password)
			user.Password = ""
			if err := db.UpdateUser(&user); err != nil {
				utils.Log.Errorf("[hash pwd for old version] failed update user: %v", err)
			}
		}
	}
}
