package _115_open

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var (
	setupDBOnce sync.Once
	setupDBErr  error
	rowSeq      int64
)

// setupTestDB mirrors internal/op/storage_test.go: an in-memory SQLite database
// behind internal/db, so op.MustSaveDriverStorage really persists and the tests
// can assert on the stored row.
func setupTestDB(t *testing.T) {
	t.Helper()
	setupDBOnce.Do(func() {
		var gormDB *gorm.DB
		gormDB, setupDBErr = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
		if setupDBErr != nil {
			return
		}
		conf.Conf = conf.DefaultConfig("testdata")
		db.Init(gormDB)
	})
	if setupDBErr != nil {
		t.Fatalf("failed to set up test database: %v", setupDBErr)
	}
}

func newTestDriver(t *testing.T, accessToken, refreshToken string) *Open115 {
	t.Helper()
	setupTestDB(t)
	rowSeq++
	addition, err := json.Marshal(Addition{AccessToken: accessToken, RefreshToken: refreshToken})
	if err != nil {
		t.Fatalf("failed to marshal addition: %v", err)
	}
	row := &model.Storage{
		Driver:    "115 Open",
		MountPath: fmt.Sprintf("/115open-test-%d", rowSeq),
		Addition:  string(addition),
	}
	if err := db.CreateStorage(row); err != nil {
		t.Fatalf("failed to create storage row: %v", err)
	}
	d := &Open115{Storage: *row}
	d.Addition.AccessToken = accessToken
	d.Addition.RefreshToken = refreshToken
	d.client = sdk.New(sdk.WithAccessToken(accessToken), sdk.WithRefreshToken(refreshToken))
	d.client.SetOnRefreshToken(d.onTokenRefreshed)
	return d
}

func persistedRefreshToken(t *testing.T, id uint) string {
	t.Helper()
	st, err := db.GetStorageById(id)
	if err != nil {
		t.Fatalf("failed to read storage back: %v", err)
	}
	var a Addition
	if err := json.Unmarshal([]byte(st.Addition), &a); err != nil {
		t.Fatalf("failed to decode persisted addition %q: %v", st.Addition, err)
	}
	return a.RefreshToken
}

func clientField(t *testing.T, c *sdk.Client, name string) string {
	t.Helper()
	v := reflect.ValueOf(c).Elem().FieldByName(name)
	if !v.IsValid() {
		t.Fatalf("field %s not found on sdk.Client", name)
	}
	return v.String()
}

// The SDK assigns the response to its own fields before invoking the callback,
// so an empty refresh token in the response must not survive in either the
// persisted addition or the running client.
func TestOnTokenRefreshedKeepsLastRefreshTokenWhenResponseOmitsIt(t *testing.T) {
	d := newTestDriver(t, "old-access", "old-refresh")

	// state the SDK leaves behind after a response without refresh_token
	d.client.SetAccessToken("")
	d.client.SetRefreshToken("")

	d.onTokenRefreshed("new-access", "")

	if got := persistedRefreshToken(t, d.ID); got != "old-refresh" {
		t.Errorf("persisted refresh token = %q, want %q", got, "old-refresh")
	}
	if got := d.Addition.RefreshToken; got != "old-refresh" {
		t.Errorf("addition refresh token = %q, want %q", got, "old-refresh")
	}
	if got := clientField(t, d.client, "refreshToken"); got != "old-refresh" {
		t.Errorf("sdk refresh token = %q, want %q", got, "old-refresh")
	}
	if got := clientField(t, d.client, "accessToken"); got != "new-access" {
		t.Errorf("sdk access token = %q, want %q", got, "new-access")
	}
}

func TestOnTokenRefreshedStoresRotatedPair(t *testing.T) {
	d := newTestDriver(t, "old-access", "old-refresh")

	d.onTokenRefreshed("new-access", "new-refresh")

	if got := persistedRefreshToken(t, d.ID); got != "new-refresh" {
		t.Errorf("persisted refresh token = %q, want %q", got, "new-refresh")
	}
	if got := clientField(t, d.client, "refreshToken"); got != "new-refresh" {
		t.Errorf("sdk refresh token = %q, want %q", got, "new-refresh")
	}
	if got := clientField(t, d.client, "accessToken"); got != "new-access" {
		t.Errorf("sdk access token = %q, want %q", got, "new-access")
	}
}
