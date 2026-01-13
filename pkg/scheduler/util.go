package scheduler

import (
	"strings"
)

// escapeTagStr escapes backslashes and colons in a string.
func escapeTagStr(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ":", "\\:")
	return s
}

// unescapeTagStr unescapes backslashes and colons in a string.
func unescapeTagStr(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			next := s[i+1]
			if next == '\\' || next == ':' {
				// Valid escaped sequence produced by escape(): unescape it.
				b.WriteByte(next)
				i++
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// splitEscapedTag splits the first unescaped colon to separate key and value.
// It expects the input to be produced by escapeTagStr.
func splitEscapedTag(tag string) (string, string, bool) {
	for i := 0; i < len(tag); i++ {
		if tag[i] == '\\' {
			i++ // Skip the escaped character
			continue
		}
		if tag[i] == ':' {
			return tag[:i], tag[i+1:], true
		}
	}
	return "", "", false
}
