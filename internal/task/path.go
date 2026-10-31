package task

import (
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// TaskWithPaths is a task that exposes virtual src/dst paths for filtering.
// Empty string means the path side is not applicable for matching.
type TaskWithPaths interface {
	TaskExtensionInfo
	GetSrcPath() string
	GetDstPath() string
}

// MatchTaskPath reports whether src or dst is equal to prefix or under prefix.
// Empty src/dst sides are ignored. prefix/src/dst are cleaned before compare.
func MatchTaskPath(src, dst, prefix string) bool {
	prefix = utils.FixAndCleanPath(prefix)
	if src != "" && utils.IsSubPath(prefix, utils.FixAndCleanPath(src)) {
		return true
	}
	if dst != "" && utils.IsSubPath(prefix, utils.FixAndCleanPath(dst)) {
		return true
	}
	return false
}
