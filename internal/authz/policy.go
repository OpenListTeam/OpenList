package authz

import (
	"path"
	"slices"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/dlclark/regexp2"
)

func CanRead(user *model.User, meta *model.Meta, reqPath string) bool {
	if user == nil {
		return true
	}
	return meta == nil || len(meta.ReadUsers) == 0 || slices.Contains(meta.ReadUsers, user.ID) ||
		!MetaCoversPath(meta.Path, reqPath, meta.ReadUsersSub)
}

func CanWrite(user *model.User, meta *model.Meta, reqPath string) bool {
	if user == nil {
		return true
	}
	return meta == nil || len(meta.WriteUsers) == 0 || slices.Contains(meta.WriteUsers, user.ID) ||
		!MetaCoversPath(meta.Path, reqPath, meta.WriteUsersSub)
}

func CanWriteContentBypassUserPerms(meta *model.Meta, reqPath string) bool {
	if meta == nil || !meta.Write {
		return false
	}
	return utils.PathEqual(meta.Path, reqPath) || meta.WSub && utils.IsSubPath(meta.Path, reqPath)
}

func IsHidden(user *model.User, meta *model.Meta, reqPath string) bool {
	return matchesHidden(hidePatterns(user, meta, path.Dir(reqPath)), path.Base(reqPath))
}

func FilterHidden(user *model.User, meta *model.Meta, parentPath string, objs []model.Obj) []model.Obj {
	patterns := hidePatterns(user, meta, parentPath)
	if len(patterns) == 0 {
		return objs
	}
	return slices.DeleteFunc(objs, func(obj model.Obj) bool {
		return matchesHidden(patterns, obj.GetName())
	})
}

func hidePatterns(user *model.User, meta *model.Meta, parentPath string) []*regexp2.Regexp {
	if user == nil || user.CanSeeHides() || meta == nil || meta.Hide == "" ||
		!MetaCoversPath(meta.Path, parentPath, meta.HSub) {
		return nil
	}
	patterns := make([]*regexp2.Regexp, 0, strings.Count(meta.Hide, "\n")+1)
	for hide := range strings.SplitSeq(meta.Hide, "\n") {
		patterns = append(patterns, regexp2.MustCompile(hide, regexp2.None))
	}
	return patterns
}

func matchesHidden(patterns []*regexp2.Regexp, name string) bool {
	for _, pattern := range patterns {
		matched, _ := pattern.MatchString(name)
		if matched {
			return true
		}
	}
	return false
}

func CanAccess(user *model.User, meta *model.Meta, reqPath, password string) bool {
	if IsHidden(user, meta, reqPath) || !CanRead(user, meta, reqPath) {
		return false
	}
	if user.CanAccessWithoutPassword() || meta == nil || meta.Password == "" {
		return true
	}
	return !MetaCoversPath(meta.Path, reqPath, meta.PSub) || meta.Password == password
}

func MetaCoversPath(metaPath, reqPath string, applyToSubFolder bool) bool {
	metaPath = utils.FixAndCleanPath(metaPath)
	reqPath = utils.FixAndCleanPath(reqPath)
	if strings.EqualFold(metaPath, reqPath) {
		return true
	}
	if !applyToSubFolder {
		return false
	}
	for reqPath != "/" {
		reqPath = path.Dir(reqPath)
		if strings.EqualFold(metaPath, reqPath) {
			return true
		}
	}
	return false
}
