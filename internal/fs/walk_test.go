package fs

import (
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func init() {
	conf.Conf = conf.DefaultConfig("data")
	database, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic(err)
	}
	db.Init(database)
}

func TestWalkFSPropagatesListFailure(t *testing.T) {
	err := WalkFS(context.Background(), 1, "/missing", &model.Object{IsFolder: true}, func(string, model.Obj) error {
		return nil
	})
	if err == nil {
		t.Fatal("WalkFS returned nil after listing failed")
	}
}
