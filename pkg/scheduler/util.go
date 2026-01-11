package scheduler

import "slices"

// sliceHasItem checks if a string exists in a slice of strings.
func sliceHasItem(slice []string, item string) bool {
	return slices.Contains(slice, item)
}
