package db

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func openTestDB(t *testing.T, prefix string) *gorm.DB {
	t.Helper()
	d, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: prefix},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// 旧版表：只有 media_items 的基础结构，没有新的组合唯一索引。
func createLegacyMediaTable(t *testing.T, d *gorm.DB, table string) {
	t.Helper()
	if err := d.Exec("CREATE TABLE " + table + " (id integer primary key autoincrement, deleted_at datetime, folder_path text, file_name text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := d.Exec("INSERT INTO " + table + " (folder_path, file_name) VALUES ('/a/b.mp3', 'b.mp3')").Error; err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, d *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := d.Table(table).Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMediaItemsTableHonorsPrefix(t *testing.T) {
	for _, prefix := range []string{"x_", "ol_", ""} {
		db = openTestDB(t, prefix)
		got, err := mediaItemsTable()
		if err != nil {
			t.Fatal(err)
		}
		if want := prefix + "media_items"; got != want {
			t.Errorf("prefix %q: table = %q, want %q", prefix, got, want)
		}
	}
}

func TestMigrateKeepsLegacyDataWithoutConfirmation(t *testing.T) {
	t.Setenv(MediaMigrateWipeEnv, "")
	db = openTestDB(t, "ol_") // 非默认前缀：旧实现硬编码 x_ 会直接跳过迁移
	createLegacyMediaTable(t, db, "ol_media_items")

	if err := migrateMediaItems(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "ol_media_items"); n != 1 {
		t.Fatalf("legacy rows must be preserved without confirmation, got %d", n)
	}
}

func TestMigrateWipesOnlyWhenConfirmed(t *testing.T) {
	t.Setenv(MediaMigrateWipeEnv, "1")
	db = openTestDB(t, "ol_")
	createLegacyMediaTable(t, db, "ol_media_items")

	if err := migrateMediaItems(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "ol_media_items"); n != 0 {
		t.Fatalf("legacy rows should be wiped after confirmation, got %d", n)
	}
}

func TestAutoMigrateCreatesCompositeUniqueIndex(t *testing.T) {
	db = openTestDB(t, "ol_")
	if err := db.AutoMigrate(&model.MediaItem{}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasIndex("ol_media_items", "idx_media_folder_file_album") {
		t.Fatal("composite unique index missing after AutoMigrate")
	}
	// 已有新索引时迁移不应触碰有效数据
	if err := db.Create(&model.MediaItem{FolderPath: "/m", FileName: "a.mp3"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := migrateMediaItems(); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, "ol_media_items"); n != 1 {
		t.Fatalf("valid rows must survive migration, got %d", n)
	}
}

func TestCreateOrUpdateRejectsOverlongIndexColumns(t *testing.T) {
	db = openTestDB(t, "ol_")
	if err := db.AutoMigrate(&model.MediaItem{}); err != nil {
		t.Fatal(err)
	}
	long := make([]rune, model.MediaIndexColumnMax+1)
	for i := range long {
		long[i] = '文'
	}
	err := CreateOrUpdateMediaItem(&model.MediaItem{FolderPath: "/m", FileName: string(long)})
	if err == nil {
		t.Fatal("overlong file_name must be rejected")
	}
	ok := make([]rune, model.MediaIndexColumnMax)
	for i := range ok {
		ok[i] = '文'
	}
	if err := CreateOrUpdateMediaItem(&model.MediaItem{FolderPath: "/m", FileName: string(ok)}); err != nil {
		t.Fatalf("boundary length must be accepted: %v", err)
	}
}
