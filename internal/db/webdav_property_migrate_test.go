package db

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/migrator"
	"gorm.io/gorm/schema"
)

// MySQL rejects indexes on TEXT columns without a key prefix length
// (Error 1170), see #3258. The unique index must therefore use the fixed-size
// hash column rather than a prefix of the full WebDAV property key.
func TestWebDAVPropertyMySQLIndexUsesKeyHash(t *testing.T) {
	conn, err := sql.Open("mysql", "user:pass@tcp(127.0.0.1:1)/db")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	d, err := gorm.Open(mysql.New(mysql.Config{Conn: conn, SkipInitializeWithVersion: true}), &gorm.Config{
		DisableAutomaticPing: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	s, err := schema.Parse(&model.WebDAVProperty{}, &sync.Map{}, d.NamingStrategy)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Path", "Namespace", "Name"} {
		if got := strings.ToLower(d.Dialector.DataTypeOf(s.LookUpField(name))); got != "text" {
			t.Fatalf("%s column type = %q, want text", name, got)
		}
	}
	if got := strings.ToLower(d.Dialector.DataTypeOf(s.LookUpField("KeyHash"))); got != "varchar(64)" {
		t.Fatalf("KeyHash column type = %q, want varchar(64)", got)
	}

	idx := s.LookIndex("idx_webdav_property")
	if idx == nil || idx.Class != "UNIQUE" {
		t.Fatalf("unique index idx_webdav_property not found: %+v", idx)
	}
	stmt := &gorm.Statement{DB: d, Schema: s}
	opts := d.Migrator().(migrator.BuildIndexOptionsInterface).BuildIndexOptions(idx.Fields, stmt)
	if len(opts) != 1 || opts[0].(clause.Expr).SQL != "`key_hash`" {
		t.Fatalf("index parts = %v, want `key_hash`", opts)
	}
}

func TestWebDAVPropertyLongPathSQLite(t *testing.T) {
	d, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.AutoMigrate(&model.WebDAVProperty{}); err != nil {
		t.Fatal(err)
	}
	if !d.Migrator().HasIndex(&model.WebDAVProperty{}, "idx_webdav_property") {
		t.Fatal("idx_webdav_property not created")
	}
	// Re-running AutoMigrate on an existing table must be a no-op.
	if err := d.AutoMigrate(&model.WebDAVProperty{}); err != nil {
		t.Fatal(err)
	}

	long := "/" + strings.Repeat("目录/", 400) + "file.txt"
	row := model.WebDAVProperty{Path: long, Namespace: "DAV:", Name: "displayname", InnerXML: []byte("x")}
	if err := d.Create(&row).Error; err != nil {
		t.Fatalf("insert long path: %v", err)
	}
	var got model.WebDAVProperty
	if err := d.Where("path = ?", long).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.Path != long {
		t.Fatalf("path truncated: len %d, want %d", len(got.Path), len(long))
	}
	if got.KeyHash != model.HashWebDAVPropertyKey(long, "DAV:", "displayname") {
		t.Fatalf("key hash = %q, want hash of complete property key", got.KeyHash)
	}
	dup := model.WebDAVProperty{Path: long, Namespace: "DAV:", Name: "displayname"}
	if err := d.Create(&dup).Error; err == nil {
		t.Fatal("duplicate (path, namespace, name) was accepted")
	}
	other := model.WebDAVProperty{Path: long + "2", Namespace: "DAV:", Name: "displayname"}
	if err := d.Create(&other).Error; err != nil {
		t.Fatal(fmt.Errorf("distinct path rejected: %w", err))
	}
}

func TestMigrateWebDAVPropertiesBackfillsKeyHash(t *testing.T) {
	d := openTestDB(t, "ol_")
	db = d
	if err := d.Exec(`CREATE TABLE ol_web_dav_properties (
		id integer primary key autoincrement,
		path text,
		namespace text,
		name text,
		lang text,
		inner_xml blob,
		UNIQUE (path, namespace, name)
	)`).Error; err != nil {
		t.Fatal(err)
	}
	path := "/" + strings.Repeat("目录/", 400) + "file.txt"
	if err := d.Exec("INSERT INTO ol_web_dav_properties (path, namespace, name) VALUES (?, ?, ?)", path, "DAV:", "displayname").Error; err != nil {
		t.Fatal(err)
	}

	if err := migrateWebDAVProperties(); err != nil {
		t.Fatal(err)
	}
	if err := d.AutoMigrate(&model.WebDAVProperty{}); err != nil {
		t.Fatal(err)
	}
	var got model.WebDAVProperty
	if err := d.Where("path = ?", path).First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.KeyHash != model.HashWebDAVPropertyKey(path, "DAV:", "displayname") {
		t.Fatalf("migrated key hash = %q, want hash of complete property key", got.KeyHash)
	}
	if !d.Migrator().HasIndex("ol_web_dav_properties", "idx_webdav_property") {
		t.Fatal("idx_webdav_property not recreated")
	}
}
