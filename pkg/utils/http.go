package utils

import (
	"fmt"
	"net/url"
	"strings"
)

// GenerateContentDisposition 生成符合RFC 5987标准的Content-Disposition头部
func GenerateContentDisposition(fileName string) string {
	// 按照RFC 2047进行编码，用于filename部分
	encodedName := urlEncode(fileName)

	// 按照RFC 5987进行编码，用于filename*部分
	encodedNameRFC5987 := encodeRFC5987(fileName)

	return fmt.Sprintf("attachment; filename=\"%s\"; filename*=utf-8''%s",
		encodedName, encodedNameRFC5987)
}

// encodeRFC5987 按照RFC 5987规范编码字符串，适用于HTTP头部参数中的非ASCII字符
func encodeRFC5987(s string) string {
	var buf strings.Builder
	for _, r := range []byte(s) {
		// 根据RFC 5987，只有字母、数字和部分特殊符号可以不编码
		if (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' || r == '.' || r == '_' || r == '~' {
			buf.WriteByte(r)
		} else {
			// 其他字符都需要百分号编码
			fmt.Fprintf(&buf, "%%%02X", r)
		}
	}
	return buf.String()
}

func urlEncode(s string) string {
	s = url.QueryEscape(s)
	s = strings.ReplaceAll(s, "+", "%20")
	return s
}
