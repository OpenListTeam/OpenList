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
// (Error 1170), see #3258. Make sure the MySQL migrator emits TEXT columns
// with prefixed index parts and that the prefixes fit the InnoDB key limit.
func TestWebDAVPropertyMySQLIndexHasPrefixLength(t *testing.T) {
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

	idx := s.LookIndex("idx_webdav_property")
	if idx == nil || idx.Class != "UNIQUE" {
		t.Fatalf("unique index idx_webdav_property not found: %+v", idx)
	}
	stmt := &gorm.Statement{DB: d, Schema: s}
	opts := d.Migrator().(migrator.BuildIndexOptionsInterface).BuildIndexOptions(idx.Fields, stmt)
	var parts []string
	totalBytes := 0
	for i, opt := range opts {
		parts = append(parts, opt.(clause.Expr).SQL)
		if idx.Fields[i].Length <= 0 {
			t.Fatalf("index field %s has no prefix length", idx.Fields[i].DBName)
		}
		totalBytes += idx.Fields[i].Length * 4 // utf8mb4
	}
	want := "`path`(512),`namespace`(128),`name`(128)"
	if got := strings.Join(parts, ","); got != want {
		t.Fatalf("index parts = %s, want %s", got, want)
	}
	if totalBytes > 3072 {
		t.Fatalf("index key is %d bytes, exceeds InnoDB limit 3072", totalBytes)
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
	dup := model.WebDAVProperty{Path: long, Namespace: "DAV:", Name: "displayname"}
	if err := d.Create(&dup).Error; err == nil {
		t.Fatal("duplicate (path, namespace, name) was accepted")
	}
	other := model.WebDAVProperty{Path: long + "2", Namespace: "DAV:", Name: "displayname"}
	if err := d.Create(&other).Error; err != nil {
		t.Fatal(fmt.Errorf("distinct path rejected: %w", err))
	}
}
