package scraper

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/sign"
)

func TestNormalizeThumbMode(t *testing.T) {
	cases := map[string]string{
		"":                ThumbModeRuntime,
		"base64":          ThumbModeBase64,
		"local":           ThumbModeVFS,
		"vfs":             ThumbModeVFS,
		"samedir":         ThumbModeSameDir,
		" same_dir ":      ThumbModeSameDir,
		"unknown-value-x": ThumbModeRuntime,
	}
	for in, want := range cases {
		if got := NormalizeThumbMode(in); got != want {
			t.Errorf("NormalizeThumbMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCoverExt(t *testing.T) {
	cases := map[string]string{
		"image/jpeg":                 ".jpg",
		"image/jpeg; charset=binary": ".jpg",
		"image/png":                  ".png",
		"image/webp":                 ".webp",
		"image/gif":                  ".gif",
		"":                           ".jpg",
	}
	for in, want := range cases {
		if got := coverExt(in); got != want {
			t.Errorf("coverExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// minimalPDF 生成一个单页 PDF（A4 页面 + 一个红色矩形）。
// 用来验证纯 Go 光栅化这条路真的能出图，而不只是编译通过。
func minimalPDF() []byte {
	return minimalPDFWithDimensions(595, 842)
}

func minimalPDFWithDimensions(width, height int) []byte {
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] "+
			"/Resources << >> /Contents 4 0 R >>", width, height),
	}
	stream := "1 0 0 rg 50 50 495 742 re f"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream))

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xref)
	return buf.Bytes()
}

func TestRenderPDFFirstPage(t *testing.T) {
	data := renderPDFFirstPage(minimalPDF())
	if len(data) < 200 {
		t.Fatalf("渲染结果太小（%d 字节），PDF 光栅化可能没生效", len(data))
	}
	// JPEG 魔数
	if data[0] != 0xFF || data[1] != 0xD8 {
		t.Fatalf("渲染结果不是 JPEG: % x", data[:4])
	}
}

func TestExtractPDFCoverDataFallsBack(t *testing.T) {
	// 非法 PDF：渲染失败，兜底扫描也找不到 JPEG，应返回空而不是 panic
	if got := extractPDFCoverData([]byte("%PDF-1.4 not really a pdf")); len(got) != 0 {
		t.Fatalf("非法 PDF 应该返回空，got %d 字节", len(got))
	}
	// 一个带内嵌 JPEG 的伪 PDF：渲染失败时应能兜底扫出图片
	payload := append([]byte("%PDF-1.4\n"), append([]byte{0xFF, 0xD8, 0xFF, 0xDB}, bytes.Repeat([]byte{0x00}, 2048)...)...)
	payload = append(payload, 0xFF, 0xD9)
	if got := extractPDFCoverData(payload); len(got) < 1024 {
		t.Fatalf("兜底扫描没取到内嵌 JPEG，got %d 字节", len(got))
	}
}

func TestCoverThumbnailDimensions(t *testing.T) {
	cases := []struct {
		width, height         int
		wantWidth, wantHeight int
	}{
		{1, 10000, 1, 300},
		{10000, 1, 300, 1},
		{10, 20, 10, 20},
		{300, 300, 300, 300},
		{595, 842, 211, 300},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			var source bytes.Buffer
			if err := png.Encode(&source, image.NewNRGBA(image.Rect(0, 0, tc.width, tc.height))); err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{
				"pdf":          renderPDFFirstPage(minimalPDFWithDimensions(tc.width, tc.height)),
				"image":        resizeCoverBytes(source.Bytes()),
				"scrape_image": NewImageScraper(true).generateThumbBytes(source.Bytes()),
			} {
				cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if cfg.Width != tc.wantWidth || cfg.Height != tc.wantHeight {
					t.Fatalf("%s: got %dx%d, want %dx%d", name, cfg.Width, cfg.Height, tc.wantWidth, tc.wantHeight)
				}
			}
			encoded := imageDataToBase64Thumb(source.Bytes())
			data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, "data:image/jpeg;base64,"))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
			if err != nil || cfg.Width != tc.wantWidth || cfg.Height != tc.wantHeight {
				t.Fatalf("base64: got %+v error=%v", cfg, err)
			}
		})
	}
}

func TestSameDirThumbPreservesExistingSidecar(t *testing.T) {
	signer := sign.NewHMACSign([]byte("thumbnail-test-token"))
	signedURL := func(path string) string {
		return thumbVFSURLWith(path, func(path string) string { return signer.Sign(path, 0) })
	}
	cases := []struct {
		name               string
		obj                model.Obj
		err                error
		overwrite          bool
		wantWrite, wantURL bool
	}{
		{"existing", &model.Object{Name: "song.flac.jpg"}, nil, false, false, true},
		{"missing", nil, errs.ObjectNotFound, false, true, true},
		{"permission", nil, errors.New("permission denied"), false, false, false},
		{"directory", &model.Object{IsFolder: true}, nil, false, false, false},
		{"explicit overwrite", &model.Object{}, nil, true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			stored := []byte("original cover")
			get := func(_ context.Context, p string, _ *fs.GetArgs) (model.Obj, error) {
				if p != "/music/song.flac.jpg" {
					t.Fatalf("lookup path=%q", p)
				}
				return tc.obj, tc.err
			}
			put := func(_ context.Context, dir string, file model.FileStreamer, _ ...bool) error {
				writes++
				if dir != "/music" || file.GetName() != "song.flac.jpg" {
					t.Fatalf("write %s/%s", dir, file.GetName())
				}
				var err error
				stored, err = io.ReadAll(file)
				return err
			}
			got := saveSameDirThumbWith(context.Background(), "/music/song.flac", "image/jpeg", []byte("new cover"), tc.overwrite, get, put, signedURL)
			if (writes > 0) != tc.wantWrite || (got != "") != tc.wantURL {
				t.Fatalf("writes=%d url=%q, want write=%v url=%v", writes, got, tc.wantWrite, tc.wantURL)
			}
			wantBytes := "original cover"
			if tc.wantWrite {
				wantBytes = "new cover"
			}
			if string(stored) != wantBytes {
				t.Fatalf("stored=%q, want %q", stored, wantBytes)
			}
		})
	}
}

func TestThumbVFSURLEncodesPathAndSignature(t *testing.T) {
	path := "/音乐/封面 #?% +&.jpg"
	signer := sign.NewHMACSign([]byte("thumbnail-test-token"))
	got := thumbVFSURLWith(path, func(path string) string { return signer.Sign(path, 0) })
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/d"+path || u.Fragment != "" {
		t.Fatalf("encoded URL lost path: %q", got)
	}
	if gotSign := u.Query().Get("sign"); gotSign != signer.Sign(path, 0) {
		t.Fatalf("decoded signature=%q", gotSign)
	}
	if err := signer.Verify(path, u.Query().Get("sign")); err != nil {
		t.Fatalf("decoded signature failed verification: %v", err)
	}
	if strings.Contains(got, " ") || !strings.Contains(got, "%23") || !strings.Contains(got, "%3F") || !strings.Contains(got, "%25") {
		t.Fatalf("path not safely encoded: %q", got)
	}
}

type trackedCoverReader struct {
	io.Reader
	reads int
}

func (r *trackedCoverReader) Read(data []byte) (int, error) {
	r.reads++
	return r.Reader.Read(data)
}

func TestScrapersPreserveRuntimeCoverBeforeWriting(t *testing.T) {
	oldDataDir := conf.DataDir
	conf.DataDir = t.TempDir()
	t.Cleanup(func() { conf.DataDir = oldDataDir })
	var imageData bytes.Buffer
	if err := png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 20, 20))); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"image", "book"} {
		t.Run(name, func(t *testing.T) {
			fileName := "photo.png"
			data := imageData.Bytes()
			if name == "book" {
				fileName = "cover.pdf"
				data = minimalPDF()
			}
			rawPath := "/library/" + fileName
			thumbPath := "imgs/" + name
			root := RuntimeThumbRoot(thumbPath)
			if err := os.MkdirAll(root, 0755); err != nil {
				t.Fatal(err)
			}
			storedPath := filepath.Join(root, thumbName(rawPath))
			oldBytes := []byte("manually selected original cover")
			if err := os.WriteFile(storedPath, oldBytes, 0644); err != nil {
				t.Fatal(err)
			}
			oldCover := "thumb:/" + thumbPath + "/" + thumbName(rawPath)
			item := &model.MediaItem{FolderPath: "/library", FileName: fileName, Cover: oldCover}
			read := &trackedCoverReader{Reader: bytes.NewReader(data)}
			var scrape func(bool, io.Reader) error
			if name == "image" {
				s := NewImageScraperWithConfig(true, ThumbModeRuntime, thumbPath)
				s.SkipEXIF = true
				scrape = func(preserve bool, r io.Reader) error {
					s.PreserveCover = preserve
					return s.ScrapeImage(item, r)
				}
			} else {
				s := NewBookLocalScraperWithConfig(ThumbModeRuntime, thumbPath)
				scrape = func(preserve bool, r io.Reader) error {
					s.PreserveCover = preserve
					return s.ScrapeBookLocal(item, r)
				}
			}
			if err := scrape(true, read); err != nil {
				t.Fatal(err)
			}
			stored, err := os.ReadFile(storedPath)
			if err != nil || !bytes.Equal(stored, oldBytes) || item.Cover != oldCover {
				t.Fatalf("preserve changed cover=%q bytes=%q error=%v", item.Cover, stored, err)
			}
			if name == "book" && read.reads != 0 {
				t.Fatalf("preserved book still read %d times", read.reads)
			}
			item.Cover = ""
			if err := scrape(false, bytes.NewReader(data)); err != nil {
				t.Fatal(err)
			}
			stored, err = os.ReadFile(storedPath)
			if err != nil || bytes.Equal(stored, oldBytes) || item.Cover != oldCover {
				t.Fatalf("allowed replacement failed: cover=%q error=%v", item.Cover, err)
			}
		})
	}
}

func TestBookPreserveCoverStopsExtractionAndStorage(t *testing.T) {
	s := NewBookLocalScraper()
	s.PreserveCover = true
	read := &trackedCoverReader{Reader: bytes.NewReader(minimalPDF())}
	if got := s.ExtractLocalCover("cover.pdf", "/cover.pdf", read); got != "" || read.reads != 0 {
		t.Fatalf("extract while preserving: cover=%q reads=%d", got, read.reads)
	}
	if got := s.storeCover("/cover.pdf", []byte("image data")); got != "" {
		t.Fatalf("stored while preserving: %q", got)
	}
}
