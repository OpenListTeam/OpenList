package handles

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func setupAccessFilterDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:handles_access_filter?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	// Swapping the handle (instead of only pointing the package global at it)
	// puts the shared database back afterwards; see test_database_test.go.
	useTestDatabase(t, database)
	// The overview caches its whole-table aggregates; a cached reply from an
	// earlier test's database would silently answer this one.
	invalidateAccessSummary()
	t.Cleanup(invalidateAccessSummary)
	return database
}

func callJSON(t *testing.T, handler gin.HandlerFunc, target string) map[string]any {
	t.Helper()
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	handler(c)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v (%s)", target, err, recorder.Body.String())
	}
	return body
}

// TestS3ObjectAccessList_RejectsBadOlderThanDays pins the reply to a parameter
// that cannot be parsed. Answering as if the filter were absent returned the
// whole table - including objects read a minute ago and objects never read at
// all - and an archiving job driven by that list moves hot objects to slow
// storage.
func TestS3ObjectAccessList_RejectsBadOlderThanDays(t *testing.T) {
	database := setupAccessFilterDB(t)
	database.Where("1 = 1").Delete(&model.S3ObjectMetadata{})

	old := time.Now().UTC().AddDate(0, 0, -400)
	fresh := time.Now().UTC().Add(-time.Minute)
	for _, row := range []model.S3ObjectMetadata{
		{Path: "/old.bin", LastAccess: &old},
		{Path: "/fresh.bin", LastAccess: &fresh},
		{Path: "/never-read.bin"},
	} {
		row := row
		if err := database.Create(&row).Error; err != nil {
			t.Fatalf("seed %s: %v", row.Path, err)
		}
	}

	for _, bad := range []string{"abc", "-5", "30d", ""} {
		if bad == "" {
			continue
		}
		body := callJSON(t, S3ObjectAccessList, "/api/admin/s3/access?older_than_days="+bad)
		if code, _ := body["code"].(float64); code != 400 {
			t.Fatalf("older_than_days=%q: expected code 400, got %v (%v)", bad, body["code"], body["message"])
		}
	}

	body := callJSON(t, S3ObjectAccessList, "/api/admin/s3/access?older_than_days=30")
	total := dataTotal(t, body)
	if total != 1 {
		t.Fatalf("expected only /old.bin, got total=%v", total)
	}

	// The flag still means "include the never-read ones" when a cutoff is given.
	body = callJSON(t, S3ObjectAccessList, "/api/admin/s3/access?older_than_days=30&include_never_read=true")
	if total := dataTotal(t, body); total != 2 {
		t.Fatalf("include_never_read: expected /old.bin and /never-read.bin, got total=%v", total)
	}
}

// TestS3ObjectAccessList_PathPrefixIsCaseSensitive pins the prefix filter to a
// byte-wise comparison: LIKE ignores case on SQLite and on MySQL's default
// collations, so a prefix naming one mount also returned objects of another.
func TestS3ObjectAccessList_PathPrefixIsCaseSensitive(t *testing.T) {
	database := setupAccessFilterDB(t)
	database.Where("1 = 1").Delete(&model.S3ObjectMetadata{})

	now := time.Now().UTC()
	for _, path := range []string{"/Movies/Hot/a.bin", "/movies/Cold/b.bin", "/Movies/100%/c.bin"} {
		row := model.S3ObjectMetadata{Path: path, LastAccess: &now}
		if err := database.Create(&row).Error; err != nil {
			t.Fatalf("seed %s: %v", path, err)
		}
	}

	body := callJSON(t, S3ObjectAccessList, "/api/admin/s3/access?path=/Movies")
	if total := dataTotal(t, body); total != 2 {
		t.Fatalf("expected the two /Movies objects, got total=%v", total)
	}
	body = callJSON(t, S3ObjectAccessList, "/api/admin/s3/access?path=/movies")
	if total := dataTotal(t, body); total != 1 {
		t.Fatalf("expected only /movies/Cold/b.bin, got total=%v", total)
	}
	body = callJSON(t, S3ObjectAccessList, "/api/admin/s3/access?path=/Movies/100%25")
	if total := dataTotal(t, body); total != 1 {
		t.Fatalf("expected the literal 100%% object, got total=%v", total)
	}
}

func dataTotal(t *testing.T, body map[string]any) float64 {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data in response: %v", body)
	}
	total, ok := data["total"].(float64)
	if !ok {
		t.Fatalf("no total in response: %v", body)
	}
	return total
}
