// Package imgmime 提供封面图片 MIME 的安全判定。
//
// 封面字节与 MIME 往往来自不可信来源（音频文件内嵌 APIC/PICTURE、远端站点的
// Content-Type）。如果把声明的 MIME 原样写进响应头，就可能把 image/svg+xml 或
// text/html 以站点同源方式交付给浏览器，造成存储型 XSS。
// 这里只信任文件头魔数，且只允许四种栅格格式。
package imgmime

import "bytes"

// Sniff 根据文件头魔数返回 image/jpeg、image/png、image/gif、image/webp 之一；
// 无法识别（包括 SVG、HTML、未知格式）时返回空串，调用方应丢弃该封面。
func Sniff(data []byte) string {
	switch {
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return "image/gif"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	default:
		return ""
	}
}

// Allowed 判断 MIME 是否属于允许对外输出的栅格图片类型。
func Allowed(mime string) bool {
	switch mime {
	case "image/jpeg", "image/png", "image/gif", "image/webp":
		return true
	default:
		return false
	}
}
