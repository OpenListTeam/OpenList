package webdav

import (
	"context"
	"encoding/xml"
	"errors"
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestLockRootHref(t *testing.T) {
	testCases := []struct {
		prefix, basePath, root string
		isDir                  bool
		want                   string
	}{
		{"/dav", "/storage", "/storage/lock-probe", true, "/dav/lock-probe/"},
		{"/dav", "/storage", "/storage/a.txt", false, "/dav/a.txt"},
		{"/dav", "/", "/storage/a.txt", false, "/dav/storage/a.txt"},
		{"/dav", "/storage", "/storage", true, "/dav/"},
		{"", "/", "/", true, "/"},
		{"/dav", "/", "/new folder/文件.txt", false, "/dav/new%20folder/%E6%96%87%E4%BB%B6.txt"},
	}
	for _, tc := range testCases {
		got := lockRootHref(tc.prefix, tc.basePath, tc.root, tc.isDir)
		if got != tc.want {
			t.Errorf("lockRootHref(%q, %q, %q, %v) = %q, want %q", tc.prefix, tc.basePath, tc.root, tc.isDir, got, tc.want)
		}
	}
}

func TestDeadPropsTransactionContextCancelDoesNotPoisonPool(t *testing.T) {
	tmpDir := t.TempDir()
	conf.Conf = conf.DefaultConfig(tmpDir)
	dbPath := filepath.Join(tmpDir, "data.db")
	database, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})
	db.Init(database)

	ctx, cancel := context.WithCancel(context.Background())
	err = withDeadPropsTransaction(ctx, func(tx *gorm.DB) error {
		if err := tx.Create(&model.WebDAVProperty{
			Path:      "/canceled",
			Namespace: "urn:test",
			Name:      "prop",
			InnerXML:  []byte("<prop>1</prop>"),
		}).Error; err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("withDeadPropsTransaction error = %v, want context.Canceled", err)
	}

	// Subsequent transaction on the same single-connection pool must succeed
	// without "cannot start a transaction within a transaction" or SQLITE_BUSY.
	err = withDeadPropsTransaction(context.Background(), func(tx *gorm.DB) error {
		return tx.Create(&model.WebDAVProperty{
			Path:      "/committed",
			Namespace: "urn:test",
			Name:      "prop",
			InnerXML:  []byte("<prop>2</prop>"),
		}).Error
	})
	if err != nil {
		t.Fatalf("subsequent withDeadPropsTransaction failed: %v", err)
	}

	canceledProps, err := getDeadProps(context.Background(), "/canceled")
	if err != nil {
		t.Fatal(err)
	}
	if len(canceledProps) != 0 {
		t.Fatalf("canceled transaction row was not rolled back: %v", canceledProps)
	}

	committedProps, err := getDeadProps(context.Background(), "/committed")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := committedProps[xml.Name{Space: "urn:test", Local: "prop"}]; !ok || string(got.InnerXML) != "<prop>2</prop>" {
		t.Fatalf("committed property = %#v, found=%v", got, ok)
	}
}
