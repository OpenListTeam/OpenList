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

// DELE and RMD go through Remove, which must behave like os.Remove: a
// missing path or a directory that still has entries is an error. SITE RMDIR
// removes a directory with its contents.
func TestRemoveRefusesMissingAndNonEmpty(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	root := t.TempDir()
	for _, dir := range []string{"full", "empty"} {
		if err := os.Mkdir(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"full/inside.txt", "file.txt"} {
		if err := os.WriteFile(filepath.Join(root, file), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
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

	exists := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	if err := Remove(ctx, "/nope.txt", false); err == nil {
		t.Error("Remove of a missing file succeeded")
	}
	if err := Remove(ctx, "/full", false); err == nil {
		t.Error("Remove of a non-empty directory succeeded")
	}
	if !exists("full/inside.txt") {
		t.Fatal("Remove deleted the contents of a non-empty directory")
	}
	if err := Remove(ctx, "/empty", false); err != nil || exists("empty") {
		t.Errorf("Remove of an empty directory: err %v, still there %v", err, exists("empty"))
	}
	if err := Remove(ctx, "/file.txt", false); err != nil || exists("file.txt") {
		t.Errorf("Remove of a file: err %v, still there %v", err, exists("file.txt"))
	}
	if err := Remove(ctx, "/full", true); err != nil || exists("full") {
		t.Errorf("recursive Remove: err %v, still there %v", err, exists("full"))
	}
}
