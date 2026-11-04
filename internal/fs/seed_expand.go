package fs

import (
	"context"
	"fmt"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
)

// seedMaxDirDepth 目录递归深度上限，防止异常的深层目录把请求拖死。
const seedMaxDirDepth = 64

// SeedSourceEntry 是展开目录后真正要打包进种子的一个文件。
type SeedSourceEntry struct {
	// Requested 用户视角路径：目录展开出来的文件为「目录/相对路径」，
	// 用于和 file_comments / share_files / direct_files 对应。
	Requested string
	// FullPath 完整 VFS 路径（已包含用户 base path）。
	FullPath string
	// SeedPath 种子内路径：保留结构时为相对 root 的路径，否则是纯文件名。
	SeedPath string
	Size     int64
	Modified string
	Obj      model.Obj
}

type rawSeedEntry struct {
	requested string
	full      string
	size      int64
	modified  string
	obj       model.Obj
}

// ExpandSeedSources 把用户选择的路径展开成待打包文件列表。
//
// 目录会被递归展开（含所有子目录里的文件）；preserve 决定种子内是否保留
// 目录结构：true 时按「相对公共父目录」的路径写入，false 时拍平成文件名，
// 重名文件会自动加序号，避免 torrent 内路径冲突。
func ExpandSeedSources(ctx context.Context, user *model.User, paths []string, preserve bool) ([]SeedSourceEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no seed source paths")
	}
	var raw []rawSeedEntry
	dirCount := 0
	for _, requestedPath := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		fullPath, err := user.JoinPath(requestedPath)
		if err != nil {
			return nil, err
		}
		meta, metaErr := op.GetNearestMeta(fullPath)
		if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
			return nil, metaErr
		}
		if !common.CanRead(user, meta, fullPath) {
			return nil, errs.PermissionDenied
		}
		if err := checkSeedSourceVisibility(user, fullPath); err != nil {
			return nil, err
		}
		obj, err := Get(ctx, fullPath, &GetArgs{NoLog: true})
		if err != nil {
			return nil, fmt.Errorf("seed path is not readable: %s: %w", requestedPath, err)
		}
		if obj.IsDir() {
			dirCount++
			if err := walkSeedDir(ctx, user, requestedPath, fullPath, "", &raw, 0); err != nil {
				return nil, err
			}
			continue
		}
		raw = append(raw, rawSeedEntry{
			requested: requestedPath,
			full:      fullPath,
			size:      obj.GetSize(),
			modified:  seedModified(obj),
			obj:       obj,
		})
		if len(raw) > torrent.DefaultMaxSeedFiles {
			return nil, fmt.Errorf("too many files to generate a seed (limit %d)", torrent.DefaultMaxSeedFiles)
		}
	}

	// 只选了一个目录时，种子根就是这个目录本身：种子名已经是目录名，
	// 文件路径必须是相对该目录的路径，否则会出现「剧/剧/S01/...」这种重复嵌套
	root := commonParentDir(paths)
	if dirCount == 1 && len(paths) == 1 {
		root = stdpath.Clean(paths[0])
	}
	if root == "" || root == "." {
		root = "/"
	}

	return finalizeSeedEntries(raw, preserve, root), nil
}

func checkSeedSourceVisibility(user *model.User, fullPath string) error {
	if user.CanSeeHides() {
		return nil
	}
	base := stdpath.Clean(user.BasePath)
	for current := stdpath.Clean(fullPath); current != "/" && current != base; current = stdpath.Dir(current) {
		parent := stdpath.Dir(current)
		meta, err := op.GetNearestMeta(parent)
		if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return err
		}
		if whetherHide(user, meta, parent) {
			merge := model.NewObjMerge()
			merge.InitHideReg(meta.Hide)
			if len(merge.Merge([]model.Obj{&model.Object{Name: stdpath.Base(current)}})) == 0 {
				return errs.PermissionDenied
			}
		}
	}
	return nil
}

// finalizeSeedEntries 根据相对 root 的路径生成种子内路径：
// preserve 时保留层级，否则只留文件名并处理重名。
func finalizeSeedEntries(raw []rawSeedEntry, preserve bool, root string) []SeedSourceEntry {
	entries := make([]SeedSourceEntry, 0, len(raw))
	for _, item := range raw {
		rel := strings.TrimPrefix(stdpath.Clean(item.requested), strings.TrimSuffix(root, "/")+"/")
		rel = strings.TrimPrefix(rel, "/")
		seedPath := rel
		if !preserve {
			seedPath = stdpath.Base(rel)
		}
		entries = append(entries, SeedSourceEntry{
			Requested: item.requested,
			FullPath:  item.full,
			SeedPath:  seedPath,
			Size:      item.size,
			Modified:  item.modified,
			Obj:       item.obj,
		})
	}
	if !preserve {
		dedupeSeedPaths(entries)
	}
	return entries
}

// walkSeedDir 递归收集目录下的文件。requestedDir 是用户视角的目录路径，
// relToDir 是当前层相对该目录的路径，用来拼出每个文件的用户视角路径。
func walkSeedDir(ctx context.Context, user *model.User, requestedDir, fullDir, relToDir string, out *[]rawSeedEntry, depth int) error {
	if depth > seedMaxDirDepth {
		return fmt.Errorf("directory nesting is too deep: %s", requestedDir)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	meta, metaErr := op.GetNearestMeta(fullDir)
	if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
		return metaErr
	}
	if !common.CanRead(user, meta, fullDir) {
		return errs.PermissionDenied
	}
	listCtx := context.WithValue(ctx, conf.UserKey, user)
	listCtx = context.WithValue(listCtx, conf.MetaKey, meta)
	objs, err := List(listCtx, fullDir, &ListArgs{NoLog: true})
	if err != nil {
		return fmt.Errorf("list %s: %w", fullDir, err)
	}
	for _, obj := range objs {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := obj.GetName()
		childRel := stdpath.Join(relToDir, name)
		childFull := stdpath.Join(fullDir, name)
		childRequested := stdpath.Join(requestedDir, childRel)
		if obj.IsDir() {
			if err := walkSeedDir(ctx, user, requestedDir, childFull, childRel, out, depth+1); err != nil {
				return err
			}
			continue
		}
		*out = append(*out, rawSeedEntry{
			requested: childRequested,
			full:      childFull,
			size:      obj.GetSize(),
			modified:  seedModified(obj),
			obj:       obj,
		})
		if len(*out) > torrent.DefaultMaxSeedFiles {
			return fmt.Errorf("too many files to generate a seed (limit %d)", torrent.DefaultMaxSeedFiles)
		}
	}
	return nil
}

// seedModified 返回文件修改时间的 RFC3339 表示，零值返回空串。
func seedModified(obj model.Obj) string {
	if obj.ModTime().IsZero() {
		return ""
	}
	return obj.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00")
}

// dedupeSeedPaths 给拍平后重名的文件加序号（a.mp3 -> a (2).mp3），
// 否则 torrent 里会出现两条相同路径。
func dedupeSeedPaths(entries []SeedSourceEntry) {
	used := make(map[string]int, len(entries))
	for i := range entries {
		entries[i].SeedPath = uniqueSeedFileName(entries[i].SeedPath, used)
	}
}

func uniqueSeedFileName(name string, used map[string]int) string {
	original := name
	ext := stdpath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for n := 2; used[name] > 0; n++ {
		name = fmt.Sprintf("%s (%d)%s", base, n, ext)
	}
	used[original]++
	if name != original {
		used[name]++
	}
	return name
}
