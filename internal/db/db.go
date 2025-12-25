package db

import (
	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

var rwDb model.Connection

func Init(d model.Connection) {
	rwDb = d
	err := AutoMigrate(new(model.Storage), new(model.User), new(model.Meta), new(model.SettingItem), new(model.SearchNode), new(model.TaskItem), new(model.SSHPublicKey), new(model.SharingDB))
	if err != nil {
		log.Fatalf("failed migrate database: %s", err.Error())
	}
}

func AutoMigrate(dst ...interface{}) error {
	var err error
	if conf.Conf.Database.Type == "mysql" {
		err = rwDb.W().Set("gorm:table_options", "ENGINE=InnoDB CHARSET=utf8mb4").AutoMigrate(dst...)
	} else {
		err = rwDb.W().AutoMigrate(dst...)
	}
	return err
}

func GetDb() model.Connection {
	return rwDb
}

func Close() {
	log.Info("closing db")
	var err error
	switch conf.Conf.Database.Type {
	case "sqlite3":
		err = rwDb.Close(true)
	default:
		err = rwDb.Close(false)
	}
	if err != nil {
		log.Errorf(err.Error())
		return
	}
}
