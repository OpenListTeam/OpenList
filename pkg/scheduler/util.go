package scheduler

import (
	"slices"
	"strings"

	"github.com/google/uuid"
)

func filterLabels(j jobsMapType, call func(j *OpJob), labels JobLabels) {
	j.ForEach(func(_ uuid.UUID, opJob *OpJob) {
		matched := true
		for k, v := range labels {
			if val, exists := opJob.Label(k); !exists || val != v {
				matched = false
				break
			}
		}
		if matched {
			call(opJob)
		}
	})
}

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
