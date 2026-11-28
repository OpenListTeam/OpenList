package modelscope

import (
	"context"
	"errors"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type ModelScope struct {
	model.Storage
	Addition
}

func (d *ModelScope) Config() driver.Config {
	return config
}

func (d *ModelScope) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *ModelScope) Init(ctx context.Context) error {
	if strings.TrimSpace(d.RepoID) == "" {
		return errors.New("repo_id is required")
	}
	if strings.TrimSpace(d.Endpoint) == "" {
		d.Endpoint = "https://modelscope.cn"
	}
	if strings.TrimSpace(d.Revision) == "" {
		d.Revision = "master"
	}
	if d.RepoType == "" {
		d.RepoType = "dataset"
	}
	if strings.TrimSpace(d.CommitMessage) == "" {
		d.CommitMessage = "Upload from OpenList"
	}
	d.RootFolderPath = utils.FixAndCleanPath(d.RootFolderPath)
	return nil
}

func (d *ModelScope) Drop(ctx context.Context) error {
	return nil
}

// repoPath maps a storage obj path into a repo-relative path.
//
// The OpenList fs layer already prefixes RootFolderPath onto obj paths (the
// driver's GetRoot returns Path = RootFolderPath), so obj.GetPath() is already
// the repo-relative path. We only normalise it.
func (d *ModelScope) repoPath(objPath string) string {
	return normalizePath(objPath)
}

func (d *ModelScope) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	repoDir := d.repoPath(dir.GetPath())

	// A directory listing only needs the direct children of repoDir. ModelScope
	// returns the full recursive tree, so we fetch it (scoped to repoDir where
	// the server supports it) and collapse to direct children.
	entries, err := d.getFileList(ctx, repoDir, true)
	if err != nil {
		return nil, err
	}

	prefix := repoDir
	if prefix != "" {
		prefix = utils.PathAddSeparatorSuffix(prefix)
	}

	seenDirs := map[string]bool{}
	objs := make([]model.Obj, 0, len(entries))
	for _, e := range entries {
		ep := normalizePath(e.getPath())
		// Keep only entries directly inside repoDir.
		if prefix != "" {
			if !strings.HasPrefix(ep, prefix) {
				continue
			}
		}
		rel := strings.TrimPrefix(ep, prefix)
		if rel == "" {
			continue
		}
		// Skip deeper nesting: only take the first path segment.
		seg := rel
		if i := strings.Index(rel, "/"); i >= 0 {
			seg = rel[:i]
		}

		name := seg
		isDir := e.isDir() || strings.Contains(rel, "/")
		if isDir {
			if seenDirs[name] {
				continue
			}
			seenDirs[name] = true
		}

		full := joinRepoPath(repoDir, name)
		// Hide our placeholder for empty directories.
		if name == ".gitkeep" {
			continue
		}
		objs = append(objs, &model.Object{
			Name:     name,
			Size:     e.getSize(),
			Modified: toTime(e.modified()),
			IsFolder: isDir,
			Path:     full,
			ID:       full,
		})
	}
	return objs, nil
}

func (d *ModelScope) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	repoFile := d.repoPath(file.GetPath())
	return &model.Link{
		URL:    d.downloadURL(repoFile),
		Header: d.downloadHeaders(),
	}, nil
}

// getRootPath returns the storage root object for this driver.
func (d *ModelScope) GetRoot(ctx context.Context) (model.Obj, error) {
	return &model.Object{
		Name:     "root",
		Path:     d.RootFolderPath,
		ID:       d.RootFolderPath,
		IsFolder: true,
	}, nil
}

func (d *ModelScope) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	// ModelScope (git-style) repositories cannot store empty directories, so we
	// create a .gitkeep placeholder file.
	dirPath := joinRepoPath(d.repoPath(parentDir.GetPath()), dirName)
	keepPath := joinRepoPath(dirPath, ".gitkeep")
	if err := d.putSmallFile(ctx, keepPath, nil); err != nil {
		return nil, err
	}
	return &model.Object{
		Name:     dirName,
		Path:     dirPath,
		ID:       dirPath,
		IsFolder: true,
	}, nil
}

func (d *ModelScope) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	filePath := joinRepoPath(d.repoPath(dstDir.GetPath()), stream.GetName())
	obj, err := d.uploadFile(ctx, filePath, stream, up)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func (d *ModelScope) Remove(ctx context.Context, obj model.Obj) error {
	repoPath := d.repoPath(obj.GetPath())
	if obj.IsDir() {
		// Removing a directory means removing every file under it.
		entries, err := d.getFileList(ctx, repoPath, true)
		if err != nil {
			return err
		}
		prefix := repoPath
		if prefix != "" {
			prefix = utils.PathAddSeparatorSuffix(prefix)
		}
		var paths []string
		for _, e := range entries {
			ep := normalizePath(e.getPath())
			if ep == repoPath {
				continue
			}
			if repoPath == "" || strings.HasPrefix(ep, prefix) {
				if !e.isDir() {
					paths = append(paths, ep)
				}
			}
		}
		if len(paths) == 0 {
			return nil
		}
		return d.deleteFiles(ctx, paths)
	}
	return d.deleteFiles(ctx, []string{repoPath})
}

func (d *ModelScope) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	src := d.repoPath(srcObj.GetPath())
	dst := joinRepoPath(path.Dir(src), newName)
	return d.movePath(ctx, src, dst, srcObj.IsDir())
}

func (d *ModelScope) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	src := d.repoPath(srcObj.GetPath())
	dst := joinRepoPath(d.repoPath(dstDir.GetPath()), srcObj.GetName())
	if dst == src {
		return srcObj, nil
	}
	if strings.HasPrefix(dst, utils.PathAddSeparatorSuffix(src)) {
		return nil, errors.New("cannot move a directory into itself")
	}
	return d.movePath(ctx, src, dst, srcObj.IsDir())
}

func (d *ModelScope) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	src := d.repoPath(srcObj.GetPath())
	dst := joinRepoPath(d.repoPath(dstDir.GetPath()), srcObj.GetName())
	if srcObj.IsDir() {
		return nil, errs.NotSupport
	}
	// Copy a single file by downloading and re-uploading.
	link, err := d.Link(ctx, srcObj, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	return d.copyFileFromURL(ctx, src, dst, link)
}

var _ driver.Driver = (*ModelScope)(nil)
