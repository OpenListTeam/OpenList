package handles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/access"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// An Emby client never asks this instance for bytes: it asks for metadata, gets a
// 302 to a /d link, and downloads from there. So the playback of a whole year is
// recorded - or not - entirely by the shape of the URL the media library hands
// out. This test pins both ends: the URL must point at the storage path (/d with
// the real path, which is what records the read), and the metadata call that
// precedes it must record nothing, because a library browsed and never played
// has to stay cold.
func embyAccessSetup(t *testing.T) (*model.User, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "movie.mkv"), []byte("0123456789"), 0o644); err != nil {
		t.Fatalf("write movie: %v", err)
	}
	database, err := gorm.Open(sqlite.Open("file:emby_access?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	useTestDatabase(t, database)
	if _, err := op.CreateStorage(context.Background(), model.Storage{
		Driver:    "Local",
		MountPath: "/vids",
		Addition:  `{"root_folder_path":"` + root + `"}`,
	}); err != nil {
		t.Fatalf("create storage: %v", err)
	}
	if err := db.SaveMediaConfig(&model.MediaConfig{MediaType: model.MediaTypeVideo, Enabled: true}); err != nil {
		t.Fatalf("enable the video library: %v", err)
	}
	item := model.MediaItem{
		MediaType:  model.MediaTypeVideo,
		FolderPath: "/vids",
		FileName:   "movie.mkv",
		FileSize:   10,
		VideoType:  "movie",
	}
	if err := db.GetDb().Create(&item).Error; err != nil {
		t.Fatalf("create media item: %v", err)
	}
	user := &model.User{Username: "viewer", BasePath: "/", Role: model.ADMIN, Permission: 0x7FFFFFFF}
	if err := db.GetDb().Create(user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	return user, "mv-" + strconv.FormatUint(uint64(item.ID), 10)
}

func embyAccessRows(t *testing.T) map[string]model.FileAccess {
	t.Helper()
	access.Flush()
	var rows []model.FileAccess
	if err := db.GetDb().Model(&model.FileAccess{}).Find(&rows).Error; err != nil {
		t.Fatalf("read file_access: %v", err)
	}
	out := make(map[string]model.FileAccess, len(rows))
	for _, row := range rows {
		out[row.Path] = row
	}
	return out
}

func TestEmbyPlaybackRecordsTheReadItHandsOut(t *testing.T) {
	user, id := embyAccessSetup(t)
	gin.SetMode(gin.TestMode)

	// Browsing the library (PlaybackInfo) must not make anything hot: it is a
	// metadata call, and a library that is listed but never played stays cold.
	{
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/emby/Items/"+id+"/PlaybackInfo", nil)
		embyPlaybackInfo(c, loadEmbyLibrary(user), id)
		if rec.Code != http.StatusOK {
			t.Fatalf("PlaybackInfo returned %d", rec.Code)
		}
		if rows := embyAccessRows(t); len(rows) != 0 {
			t.Fatalf("a metadata call recorded %d file(s) as read: %v", len(rows), rows)
		}
	}

	// The client then GETs the stream URL. What matters is where that 302 sends
	// it: it has to be the /d endpoint with the path the storage really holds,
	// because /d is what records the read.
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/emby/Videos/"+id+"/stream.mkv", nil)
	embyStream(c, user, id, false)
	if rec.Code != http.StatusFound {
		t.Fatalf("stream returned %d, want a 302 to the direct link", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("stream redirected to %q: %v", loc, err)
	}
	if !strings.HasPrefix(u.Path, "/d/") {
		t.Fatalf("stream redirected to %q, which does not go through the recording endpoint /d", loc)
	}
	// u.Path arrives percent-decoded, which is the path /d would receive.
	streamPath := strings.TrimPrefix(u.Path, "/d")
	if streamPath != "/vids/movie.mkv" {
		t.Fatalf("stream redirected to the path %q, want the storage path /vids/movie.mkv", streamPath)
	}

	// Follow it as the player would: that GET is the playback.
	if rec := downCall(t, Down, http.MethodGet, streamPath); rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Fatalf("following the stream link returned %d %q", rec.Code, rec.Body.String())
	}
	if row, ok := embyAccessRows(t)["/vids/movie.mkv"]; !ok || row.Hits != 1 {
		t.Fatalf("a played Emby item left no row on its storage path (rows: %v)", embyAccessRows(t))
	}
}
