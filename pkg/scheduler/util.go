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

// jobLabels2Tags converts JobLabels to a slice of tags.
func jobLabels2Tags(labels JobLabels) []string {
	tags := make([]string, 0, len(labels))
	if len(labels) == 0 {
		return tags
	}
	for k, v := range labels {
		tags = append(tags, escapeTagStr(k)+":"+escapeTagStr(v))
	}
	return tags
}

// tags2JobLabels converts a slice of tags to JobLabels.
func tags2JobLabels(tags []string) JobLabels {
	labels := make(JobLabels)
	if len(tags) == 0 {
		return labels
	}
	for _, tag := range tags {
		keyPart, valPart, ok := splitEscapedTag(tag)
		if !ok {
			continue
		}
		labels[unescapeTagStr(keyPart)] = unescapeTagStr(valPart)
	}
	return labels
}

// splitEscapedTag splits the first unescaped colon to separate key and value.
// It expects the input to be produced by escapeTagStr.
func splitEscapedTag(tag string) (string, string, bool) {
	for i := 0; i < len(tag); i++ {
		if tag[i] == '\\' {
			i++ // Skip the escaped character
			if i >= len(tag) {
				break
			}
			continue
		}
		if tag[i] == ':' {
			return tag[:i], tag[i+1:], true
		}
	}
	return "", "", false
}
