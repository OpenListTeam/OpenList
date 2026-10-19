// Copyright 2014 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package webdav

import (
	"context"
	stderrors "errors"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// slashClean is equivalent to but slightly more efficient than
// path.Clean("/" + name).
func slashClean(name string) string {
	if name == "" || name[0] != '/' {
		name = "/" + name
	}
	return path.Clean(name)
}

func moveResource(ctx context.Context, src, dstDir, srcDir, srcName, dstName string) error {
	if srcDir == dstDir {
		return fs.Rename(ctx, src, dstName)
	}
	if _, err := fs.Move(context.WithValue(ctx, conf.NoTaskKey, struct{}{}), src, dstDir); err != nil {
		return err
	}
	if srcName != dstName {
		return fs.Rename(ctx, path.Join(dstDir, srcName), dstName)
	}
	return nil
}

type fileMutation struct {
	rollback    func() error
	finalize    func()
	backupPaths []string
}

type fileBackup struct {
	originalPath string
	backupPath   string
	renamed      bool
}

func stagingContext(ctx context.Context) context.Context {
	ctx = context.WithValue(ctx, conf.NoTaskKey, struct{}{})
	return context.WithValue(ctx, conf.SkipHookKey, struct{}{})
}

func removeIfExists(ctx context.Context, name string) error {
	if _, err := fs.Get(ctx, name, &fs.GetArgs{}); err != nil {
		if errs.IsObjectNotFound(err) {
			return nil
		}
		return err
	}
	return fs.Remove(ctx, name)
}

func cleanupAfterFailure(ctx context.Context, name string, cause error) error {
	if err := removeIfExists(stagingContext(context.WithoutCancel(ctx)), name); err != nil {
		log.WithError(err).Errorf("failed to clean up WebDAV staging path %s", name)
		return fmt.Errorf("%w; failed to clean up %s: %w", cause, name, err)
	}
	return cause
}

func newFileBackup(ctx context.Context, originalPath string, keepOriginal bool) (*fileBackup, error) {
	backupPath := path.Join(path.Dir(originalPath), ".openlist-backup-"+uuid.NewString())
	backup := &fileBackup{originalPath: originalPath, backupPath: backupPath}
	if !keepOriginal {
		err := fs.Rename(stagingContext(ctx), originalPath, path.Base(backupPath))
		if err == nil {
			backup.renamed = true
			return backup, nil
		}
		if !errs.IsNotImplementError(err) && !errs.IsNotSupportError(err) {
			return nil, errors.WithMessage(err, "failed to rename object into rollback backup")
		}
	}
	if _, err := fs.CopyTo(stagingContext(ctx), originalPath, path.Dir(originalPath), path.Base(backupPath)); err != nil {
		return nil, cleanupAfterFailure(ctx, backupPath, errors.WithMessage(err, "failed to copy object into rollback backup"))
	}
	return backup, nil
}

func (b *fileBackup) restore(ctx context.Context) error {
	if b == nil {
		return nil
	}
	ctx = context.WithoutCancel(ctx)
	if err := removeIfExists(ctx, b.originalPath); err != nil {
		return err
	}
	if b.renamed {
		if err := fs.Rename(ctx, b.backupPath, path.Base(b.originalPath)); err != nil {
			return errors.WithMessagef(err, "failed to restore %s from %s", b.originalPath, b.backupPath)
		}
		return nil
	}
	if _, err := fs.CopyTo(context.WithValue(ctx, conf.NoTaskKey, struct{}{}), b.backupPath, path.Dir(b.originalPath), path.Base(b.originalPath)); err != nil {
		return errors.WithMessagef(err, "failed to restore %s from %s", b.originalPath, b.backupPath)
	}
	if err := fs.Remove(stagingContext(ctx), b.backupPath); err != nil {
		if !errs.IsObjectNotFound(err) {
			log.Warnf("failed to remove restored WebDAV backup %s: %v", b.backupPath, err)
		}
	}
	return nil
}

func (b *fileBackup) discard(ctx context.Context) {
	if b == nil {
		return
	}
	if err := removeIfExists(stagingContext(context.WithoutCancel(ctx)), b.backupPath); err != nil {
		log.Warnf("failed to remove WebDAV backup %s: %v", b.backupPath, err)
	}
}

func installStagedObject(ctx context.Context, stagePath, dstDir, dstName string) error {
	if err := fs.Rename(ctx, stagePath, dstName); err == nil {
		return nil
	} else if !errs.IsNotImplementError(err) && !errs.IsNotSupportError(err) {
		return err
	}
	copyCtx := context.WithValue(ctx, conf.NoTaskKey, struct{}{})
	if _, err := fs.CopyTo(copyCtx, stagePath, dstDir, dstName); err != nil {
		return err
	}
	if err := fs.Remove(stagingContext(context.WithoutCancel(ctx)), stagePath); err != nil {
		log.Warnf("failed to remove installed WebDAV staging object %s: %v", stagePath, err)
	}
	return nil
}

func copyWithRollback(ctx context.Context, src, dstDir, dstName string, dstExisted, shallow bool) (*fileMutation, error) {
	stagePath := path.Join(dstDir, ".openlist-copy-"+uuid.NewString())
	var err error
	if shallow {
		err = fs.MakeDir(stagingContext(ctx), stagePath)
	} else {
		_, err = fs.CopyTo(stagingContext(ctx), src, dstDir, path.Base(stagePath))
	}
	if err != nil {
		return nil, cleanupAfterFailure(ctx, stagePath, err)
	}
	dstPath := path.Join(dstDir, dstName)
	var dstBackup *fileBackup
	if dstExisted {
		var err error
		dstBackup, err = newFileBackup(ctx, dstPath, false)
		if err != nil {
			return nil, cleanupAfterFailure(ctx, stagePath, err)
		}
		if !dstBackup.renamed {
			if err := removeIfExists(ctx, dstPath); err != nil {
				mutation := copyMutation(ctx, dstPath, stagePath, dstBackup)
				return mutation, errors.WithMessage(err, "failed to remove destination after creating rollback copy")
			}
		}
	}
	mutation := copyMutation(ctx, dstPath, stagePath, dstBackup)
	if err := installStagedObject(ctx, stagePath, dstDir, dstName); err != nil {
		return mutation, errors.WithMessage(err, "failed to install staged COPY")
	}
	return mutation, nil
}

func copyMutation(ctx context.Context, dstPath, stagePath string, dstBackup *fileBackup) *fileMutation {
	paths := []string{stagePath}
	if dstBackup != nil {
		paths = append(paths, dstBackup.backupPath)
	}
	return &fileMutation{
		backupPaths: paths,
		rollback: func() error {
			var rollbackErrs []error
			if err := removeIfExists(context.WithoutCancel(ctx), dstPath); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
			if err := dstBackup.restore(ctx); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
			if err := removeIfExists(stagingContext(context.WithoutCancel(ctx)), stagePath); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
			return stderrors.Join(rollbackErrs...)
		},
		finalize: func() {
			if err := removeIfExists(stagingContext(context.WithoutCancel(ctx)), stagePath); err != nil {
				log.Warnf("failed to remove WebDAV COPY staging object %s: %v", stagePath, err)
			}
			dstBackup.discard(ctx)
		},
	}
}

func moveWithRollback(ctx context.Context, src, dst, dstDir, srcDir, srcName, dstName string, dstExisted bool) (*fileMutation, error) {
	// Keep a source copy so a partially completed cross-storage MOVE can be undone.
	srcBackup, err := newFileBackup(ctx, src, true)
	if err != nil {
		return nil, err
	}
	var dstBackup *fileBackup
	if dstExisted {
		dstBackup, err = newFileBackup(ctx, dst, false)
		if err != nil {
			srcBackup.discard(ctx)
			return nil, err
		}
		if !dstBackup.renamed {
			if err := removeIfExists(ctx, dst); err != nil {
				mutation := moveMutation(ctx, dst, dstDir, srcName, dstName, srcBackup, dstBackup)
				return mutation, errors.WithMessage(err, "failed to remove destination after creating rollback copy")
			}
		}
	}
	mutation := moveMutation(ctx, dst, dstDir, srcName, dstName, srcBackup, dstBackup)
	if err := moveResource(ctx, src, dstDir, srcDir, srcName, dstName); err != nil {
		return mutation, err
	}
	return mutation, nil
}

func moveMutation(ctx context.Context, dst, dstDir, srcName, dstName string, srcBackup, dstBackup *fileBackup) *fileMutation {
	paths := []string{srcBackup.backupPath}
	if dstBackup != nil {
		paths = append(paths, dstBackup.backupPath)
	}
	return &fileMutation{
		backupPaths: paths,
		rollback: func() error {
			rollbackCtx := context.WithoutCancel(ctx)
			var rollbackErrs []error
			candidates := []string{dst}
			if srcName != dstName {
				candidates = append(candidates, path.Join(dstDir, srcName))
			}
			for _, candidate := range candidates {
				if err := removeIfExists(rollbackCtx, candidate); err != nil {
					rollbackErrs = append(rollbackErrs, err)
				}
			}
			if err := srcBackup.restore(rollbackCtx); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
			if err := dstBackup.restore(rollbackCtx); err != nil {
				rollbackErrs = append(rollbackErrs, err)
			}
			return stderrors.Join(rollbackErrs...)
		},
		finalize: func() {
			srcBackup.discard(ctx)
			dstBackup.discard(ctx)
		},
	}
}

func runDeadPropsFileMutation(ctx context.Context, update func(*gorm.DB) error, apply func() (*fileMutation, error)) error {
	var mutation *fileMutation
	err := withDeadPropsTransaction(ctx, func(tx *gorm.DB) error {
		if err := update(tx); err != nil {
			return err
		}
		var err error
		mutation, err = apply()
		return err
	})
	if err != nil {
		if mutation != nil && mutation.rollback != nil {
			if rollbackErr := mutation.rollback(); rollbackErr != nil {
				log.WithError(rollbackErr).Errorf("failed to compensate WebDAV file operation; backups may remain at %s", strings.Join(mutation.backupPaths, ", "))
				return fmt.Errorf("%w; filesystem compensation failed: %w", err, rollbackErr)
			}
		}
		return err
	}
	if mutation != nil && mutation.finalize != nil {
		mutation.finalize()
	}
	return nil
}

// moveFiles moves files and/or directories from src to dst.
// Individual item permission checks are skipped for performance reasons.
//
// See section 9.9.4 for when various HTTP status codes apply.
func moveFiles(ctx context.Context, src, dst string, overwrite bool) (status int, err error) {
	srcDir := path.Dir(src)
	dstDir := path.Dir(dst)
	srcName := path.Base(src)
	dstName := path.Base(dst)
	user := ctx.Value(conf.UserKey).(*model.User)
	if srcDir != dstDir && !user.CanMove() {
		return http.StatusForbidden, nil
	}
	if srcName != dstName && !user.CanRename() {
		return http.StatusForbidden, nil
	}
	srcMeta, err := op.GetNearestMeta(srcDir)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return http.StatusInternalServerError, err
	}
	dstMeta, err := op.GetNearestMeta(dstDir)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return http.StatusInternalServerError, err
	}
	if !common.CanWrite(user, srcMeta, srcDir) || !common.CanWrite(user, dstMeta, dstDir) {
		return http.StatusForbidden, nil
	}
	if _, err = fs.Get(ctx, src, &fs.GetArgs{}); err != nil {
		if errs.IsObjectNotFound(err) {
			return http.StatusNotFound, err
		}
		return http.StatusInternalServerError, err
	}
	if dstDirInfo, err := fs.Get(ctx, dstDir, &fs.GetArgs{}); err != nil {
		if errs.IsObjectNotFound(err) {
			return http.StatusConflict, err
		}
		return http.StatusMethodNotAllowed, err
	} else if !dstDirInfo.IsDir() {
		return http.StatusConflict, nil
	}
	dstExisted := false
	if _, err = fs.Get(ctx, dst, &fs.GetArgs{}); err == nil {
		dstExisted = true
		if !overwrite {
			return http.StatusPreconditionFailed, nil
		}
	} else if !errs.IsObjectNotFound(err) {
		return http.StatusInternalServerError, err
	}
	err = runDeadPropsFileMutation(ctx,
		func(tx *gorm.DB) error { return moveDeadProps(tx, src, dst) },
		func() (*fileMutation, error) {
			return moveWithRollback(ctx, src, dst, dstDir, srcDir, srcName, dstName, dstExisted && overwrite)
		},
	)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if dstExisted {
		return http.StatusNoContent, nil
	}
	return http.StatusCreated, nil
}

// copyFiles copies files and/or directories from src to dst.
// Individual item permission checks are skipped for performance reasons.
//
// See section 9.8.5 for when various HTTP status codes apply.
func copyFiles(ctx context.Context, src, dst string, overwrite bool, depth int) (status int, err error) {
	srcDir := path.Dir(src)
	dstDir := path.Dir(dst)
	dstName := path.Base(dst)
	user := ctx.Value(conf.UserKey).(*model.User)
	if !user.CanCopy() {
		return http.StatusForbidden, nil
	}
	srcMeta, err := op.GetNearestMeta(srcDir)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return http.StatusInternalServerError, err
	}
	if !common.CanRead(user, srcMeta, srcDir) {
		return http.StatusForbidden, nil
	}
	dstMeta, err := op.GetNearestMeta(dstDir)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return http.StatusInternalServerError, err
	}
	if !common.CanWrite(user, dstMeta, dstDir) {
		return http.StatusForbidden, nil
	}
	srcInfo, err := fs.Get(ctx, src, &fs.GetArgs{})
	if err != nil {
		if errs.IsObjectNotFound(err) {
			return http.StatusNotFound, err
		}
		return http.StatusInternalServerError, err
	}
	shallow := depth == 0 && srcInfo.IsDir()
	if dstDirInfo, err := fs.Get(ctx, dstDir, &fs.GetArgs{}); err != nil {
		if errs.IsObjectNotFound(err) {
			return http.StatusConflict, err
		}
		return http.StatusMethodNotAllowed, err
	} else if !dstDirInfo.IsDir() {
		return http.StatusConflict, nil
	}
	dstExisted := false
	if _, err = fs.Get(ctx, dst, &fs.GetArgs{}); err == nil {
		dstExisted = true
		if !overwrite {
			return http.StatusPreconditionFailed, nil
		}
	} else if !errs.IsObjectNotFound(err) {
		return http.StatusInternalServerError, err
	}

	err = runDeadPropsFileMutation(ctx,
		func(tx *gorm.DB) error { return copyDeadProps(tx, src, dst) },
		func() (*fileMutation, error) {
			return copyWithRollback(ctx, src, dstDir, dstName, dstExisted && overwrite, shallow)
		},
	)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if dstExisted {
		return http.StatusNoContent, nil
	}
	return http.StatusCreated, nil
}

// walkFS traverses filesystem fs starting at name up to depth levels.
//
// Allowed values for depth are 0, 1 or infiniteDepth. For each visited node,
// walkFS calls walkFn. If a visited file system node is a directory and
// walkFn returns path.SkipDir, walkFS will skip traversal of this node.
func walkFS(ctx context.Context, depth int, name string, info model.Obj, walkFn func(reqPath string, info model.Obj, err error) error) error {
	// This implementation is based on Walk's code in the standard path/path package.
	err := walkFn(name, info, nil)
	if err != nil {
		if info.IsDir() && err == filepath.SkipDir {
			return nil
		}
		return err
	}
	if !info.IsDir() || depth == 0 {
		return nil
	}
	if depth == 1 {
		depth = 0
	}
	meta, _ := op.GetNearestMeta(name)
	// Read directory names.
	objs, err := fs.List(context.WithValue(ctx, conf.MetaKey, meta), name, &fs.ListArgs{})
	//f, err := fs.OpenFile(ctx, name, os.O_RDONLY, 0)
	//if err != nil {
	//	return walkFn(name, info, err)
	//}
	//fileInfos, err := f.Readdir(0)
	//f.Close()
	if err != nil {
		return walkFn(name, info, err)
	}

	for _, fileInfo := range objs {
		filename := path.Join(name, fileInfo.GetName())
		if err != nil {
			if err := walkFn(filename, fileInfo, err); err != nil && err != filepath.SkipDir {
				return err
			}
		} else {
			err = walkFS(ctx, depth, filename, fileInfo, walkFn)
			if err != nil {
				if !fileInfo.IsDir() || err != filepath.SkipDir {
					return err
				}
			}
		}
	}
	return nil
}
