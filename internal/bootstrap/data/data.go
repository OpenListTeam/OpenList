package data

import (
	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

func InitData() {
	initUser()
	initSettings()
	op.MigrateS3EtagModeDefault()
	initTasks()
	if flags.Dev {
		initDevData()
		initDevDo()
	}
}
