package scraper

import (
	"bytes"
	"fmt"
	"testing"
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
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] " +
			"/Resources << >> /Contents 4 0 R >>",
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
