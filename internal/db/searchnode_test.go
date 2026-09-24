package db

import (
	"path/filepath"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestDeleteSearchNodesByParentRemovesNodeAndDescendants(t *testing.T) {
	dataDir := t.TempDir()
	oldConf, oldDB := conf.Conf, db
	t.Cleanup(func() { conf.Conf, db = oldConf, oldDB })
	conf.Conf = conf.DefaultConfig(dataDir)
	database, err := gorm.Open(sqlite.Open(filepath.Join(dataDir, "search.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	Init(database)

	nodes := []model.SearchNode{
		{Parent: "/source", Name: "old.srt"},
		{Parent: "/source/old.srt", Name: "child"},
		{Parent: "/source", Name: "keep.srt"},
	}
	if err := BatchCreateSearchNodes(&nodes); err != nil {
		t.Fatal(err)
	}

	if err := DeleteSearchNodesByParent("/source/old.srt"); err != nil {
		t.Fatal(err)
	}

	remaining, err := GetSearchNodesByParent("/source")
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Name != "keep.srt" {
		t.Fatalf("remaining nodes = %#v, want only keep.srt", remaining)
	}
	descendants, err := GetSearchNodesByParent("/source/old.srt")
	if err != nil {
		t.Fatal(err)
	}
	if len(descendants) != 0 {
		t.Fatalf("remaining descendants = %#v, want none", descendants)
	}
}
