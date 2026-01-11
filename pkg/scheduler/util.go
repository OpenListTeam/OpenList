package scheduler

import (
	"slices"
	"strings"

	"github.com/google/uuid"
)

func filterLabels(j jobsMapType, call func(j *OpJob), labels ...JobLabels) {
	j.ForEach(func(_ uuid.UUID, opJob *OpJob) {
		matched := true
		for _, label := range labels {
			for k, v := range label {
				if val, exists := opJob.Label(k); !exists || val != v {
					matched = false
					break
				}
			}
			if !matched {
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
	s = strings.ReplaceAll(s, "\\\\", "\\")
	s = strings.ReplaceAll(s, "\\:", ":")
	return s
}
