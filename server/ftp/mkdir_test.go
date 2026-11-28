package ftp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// MKD of an existing directory or file fails; SITE MKDIR (MkdirAll) keeps
// os.MkdirAll's "already there is fine" for a directory.
func TestMkdirRefusesExistingPath(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	addition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := op.CreateStorage(context.Background(), model.Storage{Driver: "Local", MountPath: "/", Addition: addition}); err != nil {
		t.Fatal(err)
	}
	InitStage()
	user := &model.User{Username: "ftp-user", BasePath: "/", Role: model.GENERAL, Permission: 0x7FFF}
	ctx := context.WithValue(context.Background(), conf.UserKey, user)
	ctx = context.WithValue(ctx, conf.MetaPassKey, "")

	if err := Mkdir(ctx, "/dir"); err == nil {
		t.Error("Mkdir of an existing directory succeeded")
	}
	if err := Mkdir(ctx, "/file.txt"); err == nil {
		t.Error("Mkdir over an existing file succeeded")
	}
	if err := Mkdir(ctx, "/new"); err != nil {
		t.Errorf("Mkdir of a new directory: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "new")); err != nil || !info.IsDir() {
		t.Errorf("new directory not created: %v", err)
	}
	if err := (&AferoAdapter{ctx: ctx}).MkdirAll("/dir", 0o755); err != nil {
		t.Errorf("MkdirAll of an existing directory: %v", err)
	}
}
