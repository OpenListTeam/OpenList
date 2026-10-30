package handles

import (
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupAutoRegisterTest(t *testing.T) {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	for key, value := range map[string]string{
		conf.SSOAutoRegister:      "true",
		conf.SSODefaultPermission: "0",
		conf.SSODefaultDir:        "/",
	} {
		op.Cache.SetSetting(key, &model.SettingItem{Key: key, Value: value})
	}
	t.Cleanup(op.Cache.ClearAll)
}

// Re-login after unbinding SSO: the username is still taken by the unbound
// user, so registration must retry with the SSO ID suffix instead of failing.
func TestAutoRegisterRetriesOnUsernameConflict(t *testing.T) {
	setupAutoRegisterTest(t)
	if err := db.CreateUser(&model.User{Username: "alice"}); err != nil {
		t.Fatal(err)
	}

	user, err := autoRegister("alice", "sso-1", gorm.ErrRecordNotFound)
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if user.Username != "alice_sso-1" || user.SsoID != "sso-1" {
		t.Fatalf("unexpected user: username=%q sso_id=%q", user.Username, user.SsoID)
	}
}

func TestAutoRegisterReturnsUnrelatedErrors(t *testing.T) {
	setupAutoRegisterTest(t)
	want := errors.New("unrelated")

	user, err := autoRegister("alice", "sso-1", want)
	if user != nil || !errors.Is(err, want) {
		t.Fatalf("expected original error, got user=%v err=%v", user, err)
	}
}
