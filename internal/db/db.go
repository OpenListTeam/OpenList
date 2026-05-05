package db

import (
	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
)

var db *gorm.DB

func Init(d *gorm.DB) {
	db = d
	// 迁移前处理：删除旧的 folder_path 唯一索引（如果存在），避免迁移冲突
	// 同时清空旧的 media_items 数据（folder_path 语义已变更，旧数据不可复用）
	migrateMediaItems()
	err := AutoMigrate(new(model.Storage), new(model.User), new(model.Meta), new(model.SettingItem), new(model.SearchNode), new(model.TaskItem), new(model.SSHPublicKey), new(model.SharingDB), new(model.MediaItem), new(model.MediaConfig), new(model.MediaScanPath))
	if err != nil {
		log.Fatalf("failed migrate database: %s", err.Error())
	}
}

// migrateMediaItems 处理 media_items 表的迁移兼容性
// 存储语义已变更：folder_path 恒定为扫描根路径，file_name 为文件/文件夹名
// 唯一性由 folder_path + file_name + album_name 组合索引保证
func migrateMediaItems() {
	// 检查表是否存在
	if !db.Migrator().HasTable("x_media_items") {
		return
	}
	// 已迁移到新组合索引，跳过
	if db.Migrator().HasIndex("x_media_items", "idx_media_folder_file_album") {
		return
	}
	// 旧表存在但没有新组合索引，说明是旧版本数据，需要清空后重建
	// 旧数据的 folder_path 语义已变更（原来存完整路径，现在恒定为扫描根路径），无法复用
	log.Info("media_items: 检测到旧版本数据，清空后重新迁移（存储结构已变更）")
	// 先尝试删除旧的单字段唯一索引（如果存在），避免 AutoMigrate 冲突
	if db.Migrator().HasIndex("x_media_items", "idx_x_media_items_folder_path") {
		if err := db.Migrator().DropIndex("x_media_items", "idx_x_media_items_folder_path"); err != nil {
			log.Warnf("media_items: 删除旧唯一索引失败: %v", err)
		}
	}
	if err := db.Exec("DELETE FROM x_media_items").Error; err != nil {
		log.Warnf("media_items: 清空旧数据失败: %v", err)
	}
}

func AutoMigrate(dst ...interface{}) error {
	var err error
	if conf.Conf.Database.Type == "mysql" {
		err = db.Set("gorm:table_options", "ENGINE=InnoDB CHARSET=utf8mb4").AutoMigrate(dst...)
	} else {
		err = db.AutoMigrate(dst...)
	}
	return err
}

func GetDb() *gorm.DB {
	return db
}

func Close() {
	log.Info("closing db")
	sqlDB, err := db.DB()
	if err != nil {
		log.Errorf("failed to get db: %s", err.Error())
		return
	}
	err = sqlDB.Close()
	if err != nil {
		log.Errorf("failed to close db: %s", err.Error())
		return
	}
}
