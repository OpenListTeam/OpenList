package data

import (
	"fmt"
	"os"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

func initUser() {
	initAdmin()
	initGuest()
}

func initAdmin() {
	_, err := op.GetAdmin()
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 系统尚未初始化：仅在开发模式或显式配置 OPENLIST_ADMIN_PASSWORD 时
			// 自动创建管理员；否则交由 Web 安装向导（POST /api/public/init/setup）完成。
			adminPassword := "admin"
			envpass := os.Getenv("OPENLIST_ADMIN_PASSWORD")
			if flags.Dev {
				adminPassword = "admin"
			} else if len(envpass) > 0 {
				adminPassword = envpass
			} else {
				// 未初始化：不自动创建管理员，等待 Web 安装向导。
				return
			}
			salt := random.String(16)
			admin := &model.User{
				Username: "admin",
				Salt:     salt,
				PwdHash:  model.TwoHashPwd(adminPassword, salt),
				Role:     model.ADMIN,
				BasePath: "/",
				Authn:    "[]",
				// 0(can see hidden) - 8(webdav read) & 12(can read archives) - 14(can share)
				Permission: 0x71FF,
			}
			if err := op.CreateUser(admin); err != nil {
				panic(err)
			} else {
				// DO NOT output the password to log file. Only output to console.
				// utils.Log.Infof("Successfully created the admin user and the initial password is: %s", adminPassword)
				fmt.Printf("Successfully created the admin user and the initial password is: %s\n", adminPassword)
			}
		} else {
			utils.Log.Fatalf("[init user] Failed to get admin user: %v", err)
		}
	}
}

func initGuest() {
	_, err := op.GetGuest()
	if err == nil {
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		utils.Log.Fatalf("[init user] Failed to get guest user: %v", err)
	}
	salt := random.String(16)
	guest := &model.User{
		Username:   "guest",
		PwdHash:    model.TwoHashPwd("guest", salt),
		Salt:       salt,
		Role:       model.GUEST,
		BasePath:   "/",
		Permission: 0,
		Disabled:   true,
		Authn:      "[]",
	}
	if err := db.CreateUser(guest); err != nil {
		// The role lookup above found nothing, yet a row named `guest` already
		// occupies the unique username. Upgrading from AList is the common cause:
		// its role value lives in a legacy format nothing matches, so we would
		// re-insert forever and die on this constraint. Take the row over instead
		// of refusing to boot.
		legacy, getErr := db.GetUserByName(guest.Username)
		if getErr != nil {
			utils.Log.Fatalf("[init user] Failed to create guest user: %v", err)
		}
		legacy.Role = model.GUEST
		// Never leave the guest account usable without a password.
		legacy.Disabled = true
		if updateErr := op.UpdateUser(legacy); updateErr != nil {
			utils.Log.Fatalf("[init user] Failed to repair guest user (id=%d): %v", legacy.ID, updateErr)
		}
		utils.Log.Warnf(
			"[init user] Username %q was already taken by user id=%d and has been taken over as the disabled guest account; rename it if it used to be a regular account",
			legacy.Username, legacy.ID,
		)
	}
}
