package scheduler

import (
	"slices"
	"strings"
)

// sliceHasItem checks if a string exists in a slice of strings.
func sliceHasItem(slice []string, item string) bool {
	return slices.Contains(slice, item)
}

// escape escapes backslashes and colons in a string.
func escape(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, ":", "\\:")
	return s
}

// unescape unescapes backslashes and colons in a string.
func unescape(s string) string {
	s = strings.ReplaceAll(s, "\\\\", "\\")
	s = strings.ReplaceAll(s, "\\:", ":")
	return s
}
