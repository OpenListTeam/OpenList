package handles

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// 导出再导入：data: 封面应随压缩包带走，导入后还原成 thumb:/ 形式且文件真实落盘
func TestMediaExportZipRoundTrip(t *testing.T) {
	pngHeader := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	coverData := append(append([]byte{}, pngHeader...), bytes.Repeat([]byte{0x01}, 32)...)
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
	if err := writeMediaExportZip(&buf, &export); err != nil {
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

// 音乐封面保护：文件自带封面（data:/thumb:/站内路径）不应被刮削覆盖
func TestIsFileOwnedCover(t *testing.T) {
	for _, c := range []string{"data:image/jpeg;base64,xx", "thumb:/imgs/a.jpg", "/d/music/a.jpg?sign=1", "/p/music/a.jpg"} {
		if !isFileOwnedCover(c) {
			t.Fatalf("%q 应判定为文件自带封面", c)
		}
	}
	for _, c := range []string{"", "https://image.tmdb.org/x.jpg"} {
		if isFileOwnedCover(c) {
			t.Fatalf("%q 不应判定为文件自带封面", c)
		}
	}
}
