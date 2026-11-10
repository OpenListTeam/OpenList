package feishu

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestUserCredentialsPersistAcrossRestartAndRejectStaleWriter(t *testing.T) {
	previousConfig := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = previousConfig })
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(database)
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	cfg := Addition{RootID: driver.RootID{RootFolderID: "folder"}, AuthMode: "user", AppID: "app", AppSecret: "secret", RefreshToken: "old-refresh", RedirectURI: "https://openlist.example/api/public/feishu/callback", ReadOnly: true, DocumentFormat: "docx"}
	raw, _ := json.Marshal(cfg)
	stored := model.Storage{Driver: "Feishu", MountPath: "/feishu", Addition: string(raw), Remark: "preserve me"}
	if err := db.CreateStorage(&stored); err != nil {
		t.Fatal(err)
	}
	s := &session{cfg: cfg, storageID: stored.ID}
	if err := s.installToken(context.Background(), userToken{AccessToken: "access", RefreshToken: "rotated-refresh", ExpiresIn: 7200}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := db.GetStorageById(stored.ID)
	if err != nil {
		t.Fatal(err)
	}
	var restartCfg Addition
	if err := json.Unmarshal([]byte(reloaded.Addition), &restartCfg); err != nil {
		t.Fatal(err)
	}
	if restartCfg.RefreshToken != "rotated-refresh" || restartCfg.AccessToken != "access" || restartCfg.TokenExpiresAt < time.Now().Unix()+7000 || reloaded.Remark != "preserve me" {
		t.Fatal("rotated credentials or unrelated fields were not preserved")
	}
	if err := s.persistToken(context.Background(), "old-refresh"); err != nil {
		t.Fatalf("idempotent save retry failed: %v", err)
	}
	stale := &session{cfg: cfg, storageID: stored.ID}
	if err := stale.installToken(context.Background(), userToken{AccessToken: "stale-access", RefreshToken: "stale-refresh", ExpiresIn: 7200}); err == nil {
		t.Fatal("stale writer overwrote rotated credentials")
	}
	reloaded, _ = db.GetStorageById(stored.ID)
	if strings.Contains(reloaded.Addition, "stale-refresh") {
		t.Fatal("stale credential persisted")
	}
	// A user's default root must remain blank in saved configuration. Runtime
	// root discovery must not prevent first authorization or later token rotation.
	cfg.RootFolderID = ""
	raw, _ = json.Marshal(cfg)
	defaultRoot := model.Storage{Driver: "Feishu", MountPath: "/feishu-default-root", Addition: string(raw)}
	if err := db.CreateStorage(&defaultRoot); err != nil {
		t.Fatal(err)
	}
	defaultSession := &session{cfg: cfg, storageID: defaultRoot.ID}
	if err := defaultSession.installToken(context.Background(), userToken{AccessToken: "root-access", RefreshToken: "root-refresh", ExpiresIn: 7200}); err != nil {
		t.Fatal(err)
	}
	rootReloaded, err := db.GetStorageById(defaultRoot.ID)
	if err != nil {
		t.Fatal(err)
	}
	var rootCfg Addition
	if err := json.Unmarshal([]byte(rootReloaded.Addition), &rootCfg); err != nil || rootCfg.RootFolderID != "" || rootCfg.RefreshToken != "root-refresh" {
		t.Fatalf("invalid default-root persistence: %v", err)
	}
}
