package handles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupMediaReviewTest(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	oldConf, oldDir := conf.Conf, conf.DataDir
	conf.Conf = conf.DefaultConfig(t.TempDir())
	conf.DataDir = t.TempDir()
	db.Init(database)
	t.Cleanup(func() {
		conf.Conf, conf.DataDir = oldConf, oldDir
	})
	return database
}

func createReviewTrack(t *testing.T, name string, scanPath uint, cover string) *model.MediaItem {
	t.Helper()
	item := &model.MediaItem{MediaType: model.MediaTypeMusic, ScanPathID: scanPath, FolderPath: "/music", FileName: name,
		AlbumName: "Album", AlbumArtist: "Artist", Cover: cover, ReleaseDate: "2020", Genre: "Rock", Plot: "Plot", Description: "Description"}
	if err := db.UpdateMediaItem(item); err != nil {
		t.Fatal(err)
	}
	return item
}

func TestUploadedCoverCleanupKeepsSharedReference(t *testing.T) {
	database := setupMediaReviewTest(t)
	cover, err := saveUploadedCover(1, []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	first := createReviewTrack(t, "one.mp3", 1, cover)
	second := createReviewTrack(t, "two.mp3", 2, cover)
	second.Hidden = true
	if err := db.UpdateMediaItem(second); err != nil {
		t.Fatal(err)
	}
	first.Cover = "https://example.com/new.jpg"
	if err := db.UpdateMediaItem(first); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scraper.RuntimeThumbRoot(thumbsDirName()), filepath.Base(cover))
	removeUploadedCover(cover, first.Cover)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("shared cover removed: %v", err)
	}
	if err := database.Migrator().DropTable(&model.MediaItem{}); err != nil {
		t.Fatal(err)
	}
	removeUploadedCover(cover, "")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("cover removed after reference query failed: %v", err)
	}
	if err := database.AutoMigrate(&model.MediaItem{}); err != nil {
		t.Fatal(err)
	}
	removeUploadedCover(cover, "")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("unreferenced cover not removed: %v", err)
	}
}

func TestDeferredCoverUploadLeavesAlbumUntouched(t *testing.T) {
	setupMediaReviewTest(t)
	old, err := saveUploadedCover(1, []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	item := createReviewTrack(t, "one.mp3", 1, old)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("id", strconv.FormatUint(uint64(item.ID), 10))
	_ = writer.WriteField("deferred", "true")
	part, err := writer.CreateFormFile("file", "new.jpg")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4})
	_ = writer.Close()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/media/items/cover", &body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	UploadMediaItemCover(ctx)
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Cover string `json:"cover"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != 200 || resp.Data.Cover == old || resp.Data.Cover == "" {
		t.Fatalf("upload: %s", recorder.Body.String())
	}
	got, err := db.GetMediaItemByID(item.ID)
	if err != nil || got.Cover != old {
		t.Fatalf("deferred upload changed item: %+v, %v", got, err)
	}
	path := filepath.Join(scraper.RuntimeThumbRoot(thumbsDirName()), filepath.Base(old))
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("old cover missing: %v", err)
	}
	failed := invokeHandler(t, nil, map[string]any{"album_name": "Missing", "album_artist": "Artist", "cover": resp.Data.Cover}, UpdateMediaAlbumAdmin)
	if !strings.Contains(failed.Body.String(), `"code":404`) {
		t.Fatalf("expected failed album save: %s", failed.Body.String())
	}
	discarded := discardReviewCover(t, item.ID, resp.Data.Cover)
	if !strings.Contains(discarded.Body.String(), `"code":200`) {
		t.Fatalf("discard after failed save: %s", discarded.Body.String())
	}
	newPath := filepath.Join(scraper.RuntimeThumbRoot(thumbsDirName()), filepath.Base(resp.Data.Cover))
	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatalf("failed-save upload remains: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("rollback removed original cover: %v", err)
	}
}

func TestMediaAlbumPatchClearAndScanScope(t *testing.T) {
	setupMediaReviewTest(t)
	first := createReviewTrack(t, "one.mp3", 1, "cover-one")
	second := createReviewTrack(t, "two.mp3", 2, "cover-two")
	clear := map[string]any{"album_name": "Album", "album_artist": "Artist", "cover": "", "new_album_artist": "", "release_date": "", "genre": "", "plot": "", "description": ""}
	resp := invokeHandler(t, nil, clear, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"affected":2`) {
		t.Fatalf("update: %s", resp.Body.String())
	}
	for _, id := range []uint{first.ID, second.ID} {
		item, err := db.GetMediaItemByID(id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Cover != "" || item.AlbumArtist != "" || item.ReleaseDate != "" || item.Genre != "" || item.Plot != "" || item.Description != "" || item.AlbumName != "Album" {
			t.Fatalf("clear fields / preserve omitted field: %+v", item)
		}
	}
	resp = invokeHandler(t, nil, map[string]any{"album_name": "Album", "album_artist": "", "scan_path_id": 1, "genre": "Pop"}, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"affected":1`) {
		t.Fatalf("scoped update: %s", resp.Body.String())
	}
	for _, id := range []uint{first.ID, second.ID} {
		item, err := db.GetMediaItemByID(id)
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if id == first.ID {
			want = "Pop"
		}
		if item.Genre != want {
			t.Fatalf("scope changed wrong track: %+v", item)
		}
	}
}

func TestMediaAlbumCanUpdateEmptyIdentity(t *testing.T) {
	setupMediaReviewTest(t)
	item := createReviewTrack(t, "one.mp3", 1, "old-cover")
	resp := invokeHandler(t, nil, map[string]any{"album_name": "Album", "album_artist": "Artist", "new_album_name": "", "new_album_artist": ""}, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"affected":1`) {
		t.Fatalf("clear identity: %s", resp.Body.String())
	}
	resp = invokeHandler(t, nil, map[string]any{"album_name": "", "album_artist": "", "new_album_name": "Restored"}, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"affected":1`) {
		t.Fatalf("restore identity: %s", resp.Body.String())
	}
	got, err := db.GetMediaItemByID(item.ID)
	if err != nil || got.AlbumName != "Restored" {
		t.Fatalf("restored item: %+v, %v", got, err)
	}
	resp = invokeHandler(t, nil, map[string]any{"cover": ""}, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"code":400`) {
		t.Fatalf("missing identity accepted: %s", resp.Body.String())
	}
}

func TestReadMediaHeadDoesNotRepeatUserBasePath(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "team", "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4}
	if err := os.WriteFile(filepath.Join(root, "team", "a", "photo.jpg"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	user := setupBackslashTraversalTest(t, root, 0)
	item := &model.MediaItem{FolderPath: "/team/a", FileName: "photo.jpg"}
	if !CanUserAccessPath(user, MediaItemFilePath(item)) {
		t.Fatal("fixture image inaccessible")
	}
	got, err := readMediaHead(context.Background(), user, item, 1024)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("head = %x, error = %v", got, err)
	}
}

func discardReviewCover(t *testing.T, id uint, cover string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("id", strconv.FormatUint(uint64(id), 10))
	_ = writer.WriteField("discard", cover)
	_ = writer.Close()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/admin/media/items/cover", &body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	UploadMediaItemCover(ctx)
	return recorder
}

func TestDeferredCoverDiscardKeepsReferencesAndReclaimsOrphans(t *testing.T) {
	setupMediaReviewTest(t)
	cover, err := saveUploadedCover(42, []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 4})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(scraper.RuntimeThumbRoot(thumbsDirName()), filepath.Base(cover))
	item := createReviewTrack(t, "shared.mp3", 2, cover)
	for _, tc := range []struct { id uint; cover string; code int }{
		{43, cover, 400},
		{42, cover + "/../other.png", 400},
		{42, cover, 200},
	} {
		resp := discardReviewCover(t, tc.id, tc.cover)
		if !strings.Contains(resp.Body.String(), fmt.Sprintf(`"code":%d`, tc.code)) {
			t.Fatalf("discard: %s", resp.Body.String())
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("referenced cover removed: %v", err)
		}
	}
	item.Cover = "old-cover"
	if err := db.UpdateMediaItem(item); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		resp := discardReviewCover(t, 42, cover)
		if !strings.Contains(resp.Body.String(), `"code":200`) {
			t.Fatalf("orphan discard: %s", resp.Body.String())
		}
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("orphan remains: %v", err)
	}
}

func TestMediaAlbumRenamePreservesMixedMetadata(t *testing.T) {
	setupMediaReviewTest(t)
	first := createReviewTrack(t, "one.mp3", 1, "cover-one")
	second := createReviewTrack(t, "two.mp3", 2, "cover-two")
	second.Genre, second.Plot, second.Description, second.ReleaseDate = "Jazz", "", "Fallback", "1999"
	if err := db.UpdateMediaItem(second); err != nil {
		t.Fatal(err)
	}
	resp := invokeHandler(t, nil, map[string]any{"album_name": "Album", "album_artist": "Artist", "new_album_name": "Renamed"}, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"affected":2`) {
		t.Fatalf("rename: %s", resp.Body.String())
	}
	for _, original := range []*model.MediaItem{first, second} {
		got, err := db.GetMediaItemByID(original.ID)
		if err != nil || got.AlbumName != "Renamed" || got.Cover != original.Cover || got.Genre != original.Genre || got.Plot != original.Plot || got.Description != original.Description || got.ReleaseDate != original.ReleaseDate {
			t.Fatalf("rename changed omitted metadata: %+v, %v", got, err)
		}
	}
	resp = invokeHandler(t, nil, map[string]any{"album_name": "Renamed", "album_artist": "Artist", "plot": "", "description": ""}, UpdateMediaAlbumAdmin)
	if !strings.Contains(resp.Body.String(), `"affected":2`) {
		t.Fatalf("clear summary: %s", resp.Body.String())
	}
	got, err := db.GetMediaItemByID(second.ID)
	if err != nil || got.Plot != "" || got.Description != "" {
		t.Fatalf("summary fallback remains: %+v, %v", got, err)
	}
}

func TestMediaExifZeroGPSPresenceAndSerialization(t *testing.T) {
	for _, coords := range [][2]float64{{0, 30}, {30, 0}, {0, 0}} {
		lat, lng := coords[0], coords[1]
		resp := mediaExifResp{Latitude: &lat, Longitude: &lng}
		resp.Available = resp.hasGPS()
		encoded, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		if fields["latitude"] != lat || fields["longitude"] != lng || fields["available"] != true {
			t.Fatalf("zero GPS lost: %s", encoded)
		}
	}
	resp := mediaExifResp{}
	encoded, err := json.Marshal(resp)
	if err != nil || resp.hasGPS() || strings.Contains(string(encoded), "latitude") || strings.Contains(string(encoded), "longitude") {
		t.Fatalf("missing GPS serialized: %s, %v", encoded, err)
	}
}

func TestMediaAggregatesAuthorizeBeforeSummaryAndPaging(t *testing.T) {
	setupMediaReviewTest(t)
	if err := db.SaveMediaConfig(&model.MediaConfig{MediaType: model.MediaTypeMusic, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	root := "/review-aggregate/a"
	user := &model.User{ID: 17, BasePath: root, Role: model.GENERAL}
	for _, meta := range []*model.Meta{
		{Path: root, Hide: `^hidden\.mp3$`, HSub: true},
		{Path: root + "/private", ReadUsers: []uint{18}, ReadUsersSub: true},
		{Path: root + "/password", Password: "secret", PSub: true},
	} {
		if err := op.CreateMeta(meta); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = op.DeleteMetaById(meta.ID) })
	}
	for i, fixture := range []struct { folder, name, cover string; hidden bool }{
		{root, "one.mp3", "visible-a", false},
		{root + "/public", "two.mp3", "visible-z", false},
		{root + "/private", "read-denied.mp3", "zzz-read-secret", false},
		{root + "/password", "password-denied.mp3", "zzz-password-secret", false},
		{root + "/public", "hidden.mp3", "zzz-hidden-name", false},
		{root + "/public", "flag-hidden.mp3", "zzz-hidden-flag", true},
		{root + "2", "sibling.mp3", "zzz-sibling", false},
	} {
		item := createReviewTrack(t, fixture.name, uint(i+1), fixture.cover)
		item.FolderPath, item.Hidden = fixture.folder, fixture.hidden
		if i == 1 { item.ReleaseDate = "2022" } else if i > 1 { item.ReleaseDate = "9999" }
		if err := db.UpdateMediaItem(item); err != nil { t.Fatal(err) }
	}
	unknown := createReviewTrack(t, "unknown.mp3", 8, "unknown")
	unknown.FolderPath, unknown.AlbumName, unknown.AlbumArtist = root, "Unknown", ""
	if err := db.UpdateMediaItem(unknown); err != nil { t.Fatal(err) }
	hidden := false
	checked := make(map[string]bool)
	q := db.MediaItemQuery{PathPrefix: root, Hidden: &hidden, Page: 1, PageSize: 1,
		CanAccess: func(item *model.MediaItem) bool {
			checked[item.FileName] = true
			return CanUserAccessItem(user, item)
		}}
	albums, total, err := db.ListAlbums(q)
	if err != nil || total != 2 || len(albums) != 1 || albums[0].AlbumName != "Album" || albums[0].TrackCount != 2 || albums[0].Cover != "visible-z" || albums[0].ReleaseDate != "2022" || albums[0].ScanPathID != 2 {
		t.Fatalf("album summary exposed denied metadata or wrong pagination: %+v, %d, %v", albums, total, err)
	}
	artists, total, err := db.ListArtists(q)
	if err != nil || total != 2 || len(artists) != 1 || artists[0].Artist != "" {
		t.Fatalf("artist first page: %+v, %d, %v", artists, total, err)
	}
	q.Page = 2
	artists, total, err = db.ListArtists(q)
	if err != nil || total != 2 || len(artists) != 1 || artists[0].Artist != "Artist" || artists[0].TrackCount != 2 || artists[0].AlbumCount != 1 || artists[0].Cover != "visible-z" || artists[0].ScanPathID != 2 {
		t.Fatalf("artist summary exposed denied metadata: %+v, %d, %v", artists, total, err)
	}
	if checked["sibling.mp3"] || checked["flag-hidden.mp3"] {
		t.Fatalf("SQL prefix or hidden coarse filtering missing: %+v", checked)
	}
	q.Page, q.ArtistSet, q.Artist = 1, true, ""
	albums, total, err = db.ListAlbums(q)
	if err != nil || total != 1 || len(albums) != 1 || albums[0].AlbumArtist != "" || albums[0].AlbumName != "Unknown" {
		t.Fatalf("explicit empty artist ignored: %+v, %d, %v", albums, total, err)
	}
	q.CanAccess = func(item *model.MediaItem) bool { return CanUserAccessItem(nil, item) }
	artists, total, err = db.ListArtists(q)
	if err != nil || total != 0 || len(artists) != 0 {
		t.Fatalf("missing user must fail closed: %+v, %d, %v", artists, total, err)
	}
}

func TestMediaAggregateAccessBatchesIncludeRootFolder(t *testing.T) {
	database := setupMediaReviewTest(t)
	if err := db.SaveMediaConfig(&model.MediaConfig{MediaType: model.MediaTypeMusic, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	root := "/review-batches/a"
	meta := &model.Meta{Path: root}
	if err := op.CreateMeta(meta); err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = op.DeleteMetaById(meta.ID) })
	user := &model.User{BasePath: root, Role: model.GENERAL}
	items := make([]model.MediaItem, 0, 302)
	for i := range 300 {
		items = append(items, model.MediaItem{MediaType: model.MediaTypeMusic, FolderPath: root,
			FileName: fmt.Sprintf("track-%03d.mp3", i), AlbumName: "Album", AlbumArtist: "Artist"})
	}
	items = append(items,
		model.MediaItem{MediaType: model.MediaTypeMusic, FolderPath: "/review-batches", FileName: "a", IsFolder: true, AlbumName: "Album", AlbumArtist: "Artist"},
		model.MediaItem{MediaType: model.MediaTypeMusic, FolderPath: root + "2", FileName: "outside.mp3", AlbumName: "Outside", AlbumArtist: "Denied"})
	if err := database.Create(&items).Error; err != nil { t.Fatal(err) }
	calls := 0
	q := db.MediaItemQuery{PathPrefix: root, Page: 1, PageSize: 1,
		CanAccess: func(item *model.MediaItem) bool { calls++; return CanUserAccessItem(user, item) }}
	albums, total, err := db.ListAlbums(q)
	if err != nil || calls != 301 || total != 1 || len(albums) != 1 || albums[0].TrackCount != 301 {
		t.Fatalf("album batch/root path lost or repeated: %+v, calls=%d, total=%d, %v", albums, calls, total, err)
	}
	calls = 0
	artists, total, err := db.ListArtists(q)
	if err != nil || calls != 301 || total != 1 || len(artists) != 1 || artists[0].TrackCount != 301 || artists[0].AlbumCount != 1 {
		t.Fatalf("artist batch/root path lost or repeated: %+v, calls=%d, total=%d, %v", artists, calls, total, err)
	}
}
