package db

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"gorm.io/gorm"
)

var db *gorm.DB

// MediaMigrateWipeEnv 设为 1/true 时，允许升级流程清空旧版 media_items 数据后重建。
// 旧版本的 folder_path 语义不同，无法原地迁移；但清库是破坏性操作，必须由管理员显式确认。
const MediaMigrateWipeEnv = "OPENLIST_MEDIA_MIGRATE_WIPE"

func Init(d *gorm.DB) {
	db = d
	if err := migrateMediaItems(); err != nil {
		log.Fatalf("failed migrate media_items: %s (如果旧数据无法原地迁移，请先备份数据库，再设置 %s=1 重建媒体表)", err.Error(), MediaMigrateWipeEnv)
	}
	if err := migrateWebDAVProperties(); err != nil {
		log.Fatalf("failed migrate webdav_properties: %s", err.Error())
	}
	err := AutoMigrate(new(model.Storage), new(model.User), new(model.Meta), new(model.SettingItem), new(model.SearchNode), new(model.TaskItem), new(model.SSHPublicKey), new(model.SharingDB), new(model.WebDAVProperty), new(model.S3ObjectMetadata), new(model.FileAccess), new(model.MediaItem), new(model.MediaConfig), new(model.MediaScanPath), new(model.MediaAppKey), new(model.MediaUserData), new(model.MediaPlaylist), new(model.MediaPlaylistItem), new(model.EmbyDevice), new(model.MediaAnnotation))
	if err != nil {
		// 典型场景：未确认清库时，旧 media_items 数据违反新建的唯一索引。
		// 这里把解决办法直接写进日志，避免用户只看到一句 failed migrate database。
		log.Fatalf(
			"failed migrate database: %s (若由媒体表唯一索引与旧数据冲突导致，请先备份数据库，再设置 %s=1 重建媒体表)",
			err.Error(),
			MediaMigrateWipeEnv,
		)
	}
}

func mediaWipeConfirmed() bool {
	switch os.Getenv(MediaMigrateWipeEnv) {
	case "1", "true", "TRUE", "True", "yes":
		return true
	}
	return false
}

// TableNameOf 返回 value 对应的真实表名：带 conf.Database.TablePrefix（默认
// x_），也尊重模型自带的 TableName()。手写 SQL 表达式引用列时必须用它，
// 硬编码表名在默认前缀下会整条语句报 "no such column"。
func TableNameOf(database *gorm.DB, value interface{}) (string, error) {
	if database == nil {
		return "", gorm.ErrInvalidDB
	}
	stmt := &gorm.Statement{DB: database}
	if err := stmt.Parse(value); err != nil {
		return "", err
	}
	return stmt.Schema.Table, nil
}

// mediaItemsTable 返回 MediaItem 的真实表名。
// 表名带有 conf.Database.TablePrefix（可配置，默认 x_），不能硬编码。
func mediaItemsTable() (string, error) {
	return TableNameOf(db, &model.MediaItem{})
}

// migrateMediaItems 处理 media_items 表的迁移兼容性
// 存储语义历史变更：
//
//	v1: folder_path 为文件完整路径
//	v2: folder_path 恒定为扫描根路径
//	v3（当前）: folder_path 为文件/文件夹的实际父目录，供前端拼接播放地址
//
// 唯一性由 folder_path + file_name + album_name 组合索引保证。
//
// 检测到 v1/v2 的旧表（缺少新组合索引）时：
//   - 默认不动数据，只输出醒目的告警，并让 AutoMigrate 在旧表上补建新索引；
//   - 管理员设置环境变量 OPENLIST_MEDIA_MIGRATE_WIPE=1 后才会清空旧数据并重扫。
func migrateMediaItems() error {
	table, err := mediaItemsTable()
	if err != nil {
		return err
	}
	m := db.Migrator()
	if !m.HasTable(table) {
		return nil
	}
	const idx = "idx_media_folder_file_album"
	if m.HasIndex(table, idx) {
		// 已是新索引：仅清理软删除残留数据，避免与唯一索引冲突
		if err := db.Exec("DELETE FROM " + quoteIdent(table) + " WHERE deleted_at IS NOT NULL").Error; err != nil {
			log.Warnf("media_items: 清理软删除残留数据失败: %v", err)
		}
		return nil
	}

	// 旧表存在但没有新组合索引：旧数据的 folder_path 语义已变更，无法复用。
	if !mediaWipeConfirmed() {
		log.Warnf("media_items: 检测到旧版本数据（表 %s 缺少索引 %s）。", table, idx)
		log.Warnf("media_items: 旧数据的路径语义已变更，建议清空后重新扫描；为避免误删，本次启动不会自动清库。")
		log.Warnf("media_items: 如确认清空，请设置环境变量 %s=1 后重启；也可在管理后台对各媒体库点击「清空数据库」再重扫。", MediaMigrateWipeEnv)
		// AutoMigrate 随后要在旧数据上补建 (folder_path, file_name, album_name)
		// 组合唯一索引。旧数据若有重复行，建索引会失败，Init 里的 Fatalf 会
		// 把进程直接杀掉——而容器编排里重启不会自动带上环境变量，实例就陷入
		// crash-loop。清空整表必须管理员确认，但“重复行”是同一个条目的多条记
		// 录，保留每组最新一条即可让索引建起来、实例先启动；语义修正交给之后
		// 的重扫。软删除残留同样会撞唯一索引，一并清掉（新索引分支也这么做）。
		if err := dedupeLegacyMediaItems(table); err != nil {
			return err
		}
		return nil
	}
	log.Warnf("media_items: 已确认 %s，清空旧版本数据（表 %s）后重新迁移", MediaMigrateWipeEnv, table)
	// 先尝试删除旧的单字段唯一索引（如果存在），避免 AutoMigrate 冲突
	oldIdx := "idx_" + table + "_folder_path"
	if m.HasIndex(table, oldIdx) {
		if err := m.DropIndex(table, oldIdx); err != nil {
			log.Warnf("media_items: 删除旧唯一索引失败: %v", err)
		}
	}
	if err := db.Exec("DELETE FROM " + quoteIdent(table)).Error; err != nil {
		return err
	}
	return nil
}

// dedupeLegacyMediaItems 让旧表能通过组合唯一索引的创建：
//   - 删除软删除残留（deleted_at 非空的行，新索引分支同样清理）；
//   - 对 (folder_path, file_name, album_name) 相同的每组只保留 id 最大（最新）的一行。
//
// 只丢弃重复与已删除的行，不碰唯一记录；若这一步之后 AutoMigrate 仍失败，
// 说明问题不在重复行上，按原有 Fatalf 路径处理。
func dedupeLegacyMediaItems(table string) error {
	q := quoteIdent(table)
	m := db.Migrator()
	for _, col := range []string{"deleted_at", "id", "folder_path", "file_name", "album_name"} {
		if !m.HasColumn(table, col) {
			// 比去重依赖的列还老的表：AutoMigrate 会先补列（旧行全 NULL，不
			// 撞唯一索引），这里无需也无法去重。
			log.Debugf("media_items: legacy table %s lacks column %s, skipping dedupe", table, col)
			return nil
		}
	}
	if err := db.Exec("DELETE FROM " + q + " WHERE deleted_at IS NOT NULL").Error; err != nil {
		return err
	}
	// MySQL 不允许 DELETE 直接引用同表的子查询，包一层派生表后三种方言通用。
	dedup := "DELETE FROM " + q + " WHERE id NOT IN (SELECT max_id FROM (SELECT MAX(id) AS max_id FROM " + q + " GROUP BY folder_path, file_name, album_name) AS keep_newest)"
	if err := db.Exec(dedup).Error; err != nil {
		return err
	}
	return nil
}

// quoteIdent 按当前数据库方言给标识符加引号，表名来自 gorm schema 解析而非用户输入。
func quoteIdent(name string) string {
	var sb strings.Builder
	db.Dialector.QuoteTo(&sb, name)
	return sb.String()
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
