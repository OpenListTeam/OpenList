package handles

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/gin-gonic/gin"
)

// 导出再导入：data: 封面应随压缩包带走，导入后还原成 thumb:/ 形式且文件真实落盘
func TestMediaExportZipRoundTrip(t *testing.T) {
	setupMediaExportRoot(t)
	coverData := mediaTestPNG(t)
	cover := "data:image/png;base64," + base64.StdEncoding.EncodeToString(coverData)

	export := MediaExportData{
		Version:   mediaExportVersion,
		ExportAt:  time.Now(),
		Scope:     "media_type",
		MediaType: model.MediaTypeVideo,
		Items: []model.MediaItem{
			{ID: 1, FileName: "Inception.mkv", Cover: cover},
		},
	}

	var buf bytes.Buffer
	if err := writeMediaExportZip(context.Background(), &buf, &export); err != nil {
		t.Fatalf("write zip: %v", err)
	}
	if !isZipPayload(buf.Bytes()) {
		t.Fatal("导出结果不是 zip")
	}
	// 导出后 JSON 里的 cover 应改成压缩包内相对路径
	if !strings.HasPrefix(export.Items[0].Cover, mediaExportCoverDir+"/") {
		t.Fatalf("cover = %q, want covers/...", export.Items[0].Cover)
	}

	data, err := readMediaExportZip(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if len(data.Items) != 1 {
		t.Fatalf("items = %d", len(data.Items))
	}
	got := data.Items[0].Cover
	if !strings.HasPrefix(got, "thumb:/") {
		t.Fatalf("导入后的 cover = %q, want thumb:/...", got)
	}
	// 封面文件应真实写到缩略图目录
	rel := strings.TrimPrefix(got, "thumb:/")
	parts := strings.SplitN(rel, "/", 2)
	if len(parts) != 2 {
		t.Fatalf("cover = %q", got)
	}
	path := filepath.Join(scraper.RuntimeThumbRoot(parts[0]), parts[1])
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读封面文件失败: %v", err)
	}
	if !bytes.Equal(raw, coverData) {
		t.Fatal("封面内容与导出不一致")
	}
	_ = os.Remove(path)
}

// 非图片内容不能当封面上传
func TestCoverFileExtRejectsNonImage(t *testing.T) {
	if ext := coverFileExt([]byte("<html>not image</html>")); ext != "" {
		t.Fatalf("coverFileExt(html) = %q, want empty", ext)
	}
	if ext := coverFileExt([]byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0, 0, 0}); ext != ".jpg" {
		t.Fatalf("coverFileExt(jpeg) = %q", ext)
	}
}

func setupMediaExportRoot(t *testing.T) string {
	t.Helper()
	previous := conf.DataDir
	conf.DataDir = t.TempDir()
	t.Cleanup(func() { conf.DataDir = previous })
	return scraper.RuntimeThumbRoot(thumbsDirName())
}

func mediaTestPNG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type mediaZipEntry struct {
	name string
	data []byte
}

func mediaTestZip(t *testing.T, data MediaExportData, entries ...mediaZipEntry) []byte {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	entries = append(entries, mediaZipEntry{mediaExportJSONName, raw})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		w, err := zw.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestMediaImportMultipartZipAndLegacyJSON(t *testing.T) {
	setupMediaExportRoot(t)
	original := MediaExportData{Version: mediaExportVersion, Items: []model.MediaItem{{FileName: "film.mkv", Cover: "covers/a.png"}}}
	zipPayload := mediaTestZip(t, original, mediaZipEntry{"covers/a.png", mediaTestPNG(t)})
	legacy := MediaExportData{Version: 1, Items: []model.MediaItem{{FileName: "legacy.mkv"}}}
	legacyJSON, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		payload []byte
		want    string
	}{
		{"zip", zipPayload, "film.mkv"},
		{"legacy JSON", legacyJSON, "legacy.mkv"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			part, err := mw.CreateFormFile("file", "export")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write(tt.payload); err != nil {
				t.Fatal(err)
			}
			if err := mw.Close(); err != nil {
				t.Fatal(err)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(body.Bytes()))
			c.Request.Header.Set("Content-Type", mw.FormDataContentType())
			got, covers, err := readMediaImportRequest(c, maxImportBytes)
			for _, cover := range covers {
				defer os.Remove(cover.path)
			}
			if err != nil || got == nil {
				t.Fatalf("multipart import: %v", err)
			}
			if len(got.Items) != 1 || got.Items[0].FileName != tt.want {
				t.Fatalf("items = %+v", got.Items)
			}
			if tt.name == "zip" && !strings.HasPrefix(got.Items[0].Cover, "thumb:/") {
				t.Fatalf("ZIP cover was not imported: %q", got.Items[0].Cover)
			}
		})
	}
}

func TestMediaImportRequestLimitsWholeMultipart(t *testing.T) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "export.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(`{"version":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := mw.WriteField("ignored", strings.Repeat("x", 4096)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/import", bytes.NewReader(body.Bytes()))
	c.Request.ContentLength = -1
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	_, _, err = readMediaImportRequest(c, 1024)
	var sizeErr *http.MaxBytesError
	if !errors.As(err, &sizeErr) {
		t.Fatalf("whole multipart limit: %v", err)
	}
	c, _ = gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/import", strings.NewReader(`{"version":1,"scope":"`+strings.Repeat("x", 100)+`"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	if _, _, err := readMediaImportRequest(c, 32); !errors.As(err, &sizeErr) {
		t.Fatalf("JSON request limit: %v", err)
	}
}

func TestMediaImportZipBudgetsAndRollback(t *testing.T) {
	pngData := mediaTestPNG(t)
	original := MediaExportData{Version: mediaExportVersion, Items: []model.MediaItem{{Cover: "covers/a.png"}, {Cover: "covers/b.png"}}}
	for _, tt := range []struct {
		name   string
		limits mediaImportLimits
		bad    []byte
	}{
		{"entries", mediaImportLimits{2, maxImportJSONBytes, maxExportCoverBytes}, pngData},
		{"JSON bytes", mediaImportLimits{maxImportEntries, 16, maxExportCoverBytes}, pngData},
		{"cover total", mediaImportLimits{maxImportEntries, maxImportJSONBytes, int64(len(pngData))}, pngData},
		{"invalid image rollback", defaultMediaImportLimits(), []byte("<html>not an image</html>")},
		{"truncated image rollback", defaultMediaImportLimits(), pngData[:len(pngData)/2]},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := setupMediaExportRoot(t)
			payload := mediaTestZip(t, original, mediaZipEntry{"covers/a.png", pngData}, mediaZipEntry{"covers/b.png", tt.bad})
			if _, _, err := readMediaExportZipWithBudget(context.Background(), bytes.NewReader(payload), int64(len(payload)), tt.limits); err == nil {
				t.Fatal("expected rejected archive")
			}
			files, err := os.ReadDir(root)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if len(files) != 0 {
				t.Fatalf("failed import left files: %+v", files)
			}
		})
	}
}

func TestMediaImportZipOnlyReferencedRasterCovers(t *testing.T) {
	root := setupMediaExportRoot(t)
	original := MediaExportData{Version: mediaExportVersion, Items: []model.MediaItem{{Cover: "covers/a.png"}, {Cover: "covers/a.png"}, {Cover: "https://image.tmdb.org/original.png"}}}
	payload := mediaTestZip(t, original,
		mediaZipEntry{"covers/a.png", mediaTestPNG(t)},
		mediaZipEntry{"covers/orphan.png", mediaTestPNG(t)},
		mediaZipEntry{"covers/evil.jpg", []byte("<script>invalid</script>")})
	got, err := readMediaExportZip(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 1 {
		t.Fatalf("saved files = %v, err = %v", files, err)
	}
	if got.Items[0].Cover != got.Items[1].Cover || got.Items[2].Cover != original.Items[2].Cover {
		t.Fatalf("reference compatibility: %+v", got.Items)
	}
	for _, name := range []string{"covers/../a.png", `covers/..\a.png`, "covers/C:/a.png", "covers/missing.png"} {
		original.Items = []model.MediaItem{{Cover: name}}
		payload = mediaTestZip(t, original, mediaZipEntry{name, mediaTestPNG(t)})
		if name == "covers/missing.png" {
			payload = mediaTestZip(t, original)
		}
		if _, err := readMediaExportZip(bytes.NewReader(payload), int64(len(payload))); err == nil {
			t.Fatalf("accepted invalid cover reference %q", name)
		}
	}
	original.Version = mediaExportVersion + 1
	payload = mediaTestZip(t, original)
	if _, err := readMediaExportZip(bytes.NewReader(payload), int64(len(payload))); err == nil {
		t.Fatal("accepted unsupported version")
	}
	original.Version = mediaExportVersion
	payload = mediaTestZip(t, original, mediaZipEntry{mediaExportJSONName, []byte(`{"version":2}`)})
	if _, err := readMediaExportZip(bytes.NewReader(payload), int64(len(payload))); err == nil {
		t.Fatal("accepted duplicate JSON entry")
	}
}

func TestMediaImportZipCountsActualDirectoryEntries(t *testing.T) {
	original := MediaExportData{Version: mediaExportVersion}
	payload := mediaTestZip(t, original, mediaZipEntry{"one", nil}, mediaZipEntry{"two", nil})
	end := len(payload) - 22
	payload[end+8], payload[end+9] = 1, 0
	payload[end+10], payload[end+11] = 1, 0
	if err := checkMediaZipEntryBudget(context.Background(), bytes.NewReader(payload), int64(len(payload)), 2); err == nil {
		t.Fatal("trusted forged entry count instead of counting the central directory")
	}
	binary.LittleEndian.PutUint32(payload[end+12:], uint32(46+len(mediaExportJSONName)))
	if err := checkMediaZipEntryBudget(context.Background(), bytes.NewReader(payload), int64(len(payload)), 2); err == nil {
		t.Fatal("forged directory size bypassed the actual entry budget")
	}
}

func TestMediaImportZip64EntryBudgetCompatibility(t *testing.T) {
	payload := mediaTestZip(t, MediaExportData{Version: mediaExportVersion})
	end := len(payload) - 22
	eocd := append([]byte(nil), payload[end:]...)
	var record [56]byte
	binary.LittleEndian.PutUint32(record[:], 0x06064b50)
	binary.LittleEndian.PutUint64(record[4:], 44)
	binary.LittleEndian.PutUint16(record[12:], 45)
	binary.LittleEndian.PutUint16(record[14:], 45)
	binary.LittleEndian.PutUint64(record[24:], 1)
	binary.LittleEndian.PutUint64(record[32:], 1)
	binary.LittleEndian.PutUint64(record[40:], uint64(binary.LittleEndian.Uint32(eocd[12:])))
	binary.LittleEndian.PutUint64(record[48:], uint64(binary.LittleEndian.Uint32(eocd[16:])))
	var locator [20]byte
	binary.LittleEndian.PutUint32(locator[:], 0x07064b50)
	binary.LittleEndian.PutUint64(locator[8:], uint64(end))
	binary.LittleEndian.PutUint32(locator[16:], 1)
	binary.LittleEndian.PutUint16(eocd[8:], 0xffff)
	binary.LittleEndian.PutUint16(eocd[10:], 0xffff)
	binary.LittleEndian.PutUint32(eocd[12:], 0xffffffff)
	binary.LittleEndian.PutUint32(eocd[16:], 0xffffffff)
	payload = append(payload[:end], record[:]...)
	payload = append(payload, locator[:]...)
	payload = append(payload, eocd...)
	data, err := readMediaExportZip(bytes.NewReader(payload), int64(len(payload)))
	if err != nil || data == nil || data.Version != mediaExportVersion {
		t.Fatalf("valid bounded ZIP64 import: data=%+v err=%v", data, err)
	}
	if err := checkMediaZipEntryBudget(context.Background(), bytes.NewReader(payload), int64(len(payload)), 0); err == nil {
		t.Fatal("ZIP64 entry budget was not enforced")
	}
}

func TestCoverBytesFixedRootAndLimits(t *testing.T) {
	root := setupMediaExportRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	pngData := mediaTestPNG(t)
	if err := os.WriteFile(filepath.Join(root, "nested", "valid.png"), pngData, 0o644); err != nil {
		t.Fatal(err)
	}
	prefix := "thumb:/" + thumbsDirName() + "/"
	if got, ext := coverBytes(context.Background(), prefix+"nested/valid.png"); !bytes.Equal(got, pngData) || ext != ".png" {
		t.Fatalf("valid nested cover: len=%d ext=%q", len(got), ext)
	}
	for _, cover := range []string{
		"thumb:/other/nested/valid.png", prefix + "../nested/valid.png", prefix + `..\valid.png`,
		prefix + "C:/valid.png", prefix + `/valid.png`, prefix + `nested\valid.png`, prefix + ".. /valid.png",
		"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString([]byte("<html>not image</html>")),
		"data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(maxCoverBytes)+1),
	} {
		if got, _ := coverBytes(context.Background(), cover); len(got) != 0 {
			t.Fatalf("accepted invalid cover %q", cover[:min(len(cover), 80)])
		}
	}
	large, err := os.Create(filepath.Join(root, "large.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(maxCoverBytes + 1); err != nil {
		_ = large.Close()
		t.Fatal(err)
	}
	_ = large.Close()
	if got, _ := coverBytes(context.Background(), prefix+"large.png"); len(got) != 0 {
		t.Fatal("accepted oversized local file")
	}
}

func TestCoverBytesRejectsSymlinkEscape(t *testing.T) {
	root := setupMediaExportRoot(t)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, mediaTestPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape.png")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if got, _ := coverBytes(context.Background(), "thumb:/"+thumbsDirName()+"/escape.png"); len(got) != 0 {
		t.Fatal("accepted symlink outside configured root")
	}
}

type mediaTestTransport func(*http.Request) (*http.Response, error)

func (f mediaTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func mediaSetTransport(t *testing.T, transport http.RoundTripper) {
	t.Helper()
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
}

func TestExportCoverRemoteValidationAndRedirects(t *testing.T) {
	pngData := mediaTestPNG(t)
	for _, tt := range []struct {
		name     string
		initial  string
		location string
		calls    int32
		want     bool
	}{
		{"blocked initial", "http://127.0.0.1/a.png", "", 0, false},
		{"blocked redirect", "https://image.tmdb.org/a.png", "http://localhost/a.png", 1, false},
		{"allowed redirect", "https://image.tmdb.org/a.png", "https://img.doubanio.com/a.png", 2, true},
		{"allowed source", "https://image.tmdb.org/a.png", "", 1, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			mediaSetTransport(t, mediaTestTransport(func(r *http.Request) (*http.Response, error) {
				if calls.Add(1) == 1 && tt.location != "" {
					return &http.Response{StatusCode: http.StatusFound, Header: http.Header{"Location": []string{tt.location}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(pngData)), Request: r}, nil
			}))
			got, _ := coverBytes(context.Background(), tt.initial)
			if (len(got) > 0) != tt.want || calls.Load() != tt.calls {
				t.Fatalf("len=%d calls=%d, want success=%v calls=%d", len(got), calls.Load(), tt.want, tt.calls)
			}
		})
	}
}

func TestExportCoverBudgetStopsScheduling(t *testing.T) {
	pngData := mediaTestPNG(t)
	var calls atomic.Int32
	mediaSetTransport(t, mediaTestTransport(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(pngData)), Request: r}, nil
	}))
	items := make([]model.MediaItem, 100)
	for i := range items {
		items[i].Cover = "https://image.tmdb.org/a.png"
	}
	got := fetchCoversWithBudget(context.Background(), items, maxExportCovers, len(pngData))
	if len(got) != 1 || calls.Load() > coverFetchWorkers {
		t.Fatalf("budget did not stop scheduling: covers=%d calls=%d", len(got), calls.Load())
	}
	calls.Store(0)
	got = fetchCoversWithBudget(context.Background(), items, 2, maxExportCoverBytes)
	if len(got) != 2 || calls.Load() != 2 {
		t.Fatalf("count budget: covers=%d calls=%d", len(got), calls.Load())
	}
}

func TestExportCoverCancellationAndOriginalURL(t *testing.T) {
	started := make(chan struct{}, 1)
	mediaSetTransport(t, mediaTestTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fetchCovers(ctx, []model.MediaItem{{Cover: "https://image.tmdb.org/a.png"}})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("fetch did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("fetch did not stop after request cancellation")
	}
	var buf bytes.Buffer
	data := MediaExportData{Version: mediaExportVersion}
	if err := writeMediaExportZip(ctx, &buf, &data); !errors.Is(err, context.Canceled) || buf.Len() != 0 {
		t.Fatalf("cancelled ZIP: err=%v bytes=%d", err, buf.Len())
	}
	data.Items = []model.MediaItem{{Cover: "https://unsupported.example/a.png"}}
	if err := writeMediaExportZip(context.Background(), &buf, &data); err != nil {
		t.Fatal(err)
	}
	if data.Items[0].Cover != "https://unsupported.example/a.png" {
		t.Fatal("failed/unsupported remote cover lost its original URL")
	}
}

func TestPrepareMediaScrapeCandidateProtectsBeforeScraping(t *testing.T) {
	for _, cover := range []string{"thumb:/imgs/old.jpg", "https://image.tmdb.org/old.jpg", "data:image/png;base64,old"} {
		original := model.MediaItem{Cover: cover}
		candidate, preserve := prepareMediaScrapeCandidate(original, false)
		if !preserve || candidate.Cover != cover {
			t.Fatalf("cover was not protected before scraping: %+v, preserve=%v", candidate, preserve)
		}
		candidate.Cover = "changed"
		if original.Cover != cover {
			t.Fatal("candidate modified original item")
		}
		candidate, preserve = prepareMediaScrapeCandidate(original, true)
		if preserve || candidate.Cover != "" {
			t.Fatal("explicit overwrite did not permit a replacement candidate")
		}
	}
	if _, preserve := prepareMediaScrapeCandidate(model.MediaItem{}, false); preserve {
		t.Fatal("empty cover should permit scraping")
	}
}
