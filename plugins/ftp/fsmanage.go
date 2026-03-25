package ftp

import (
	"context"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
)

func Mkdir(ctx context.Context, path string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	reqPath, err := user.JoinPath(path)
	if err != nil {
		return err
	}
	if !user.CanWrite() || !user.CanFTPManage() {
		meta, err := op.GetNearestMeta(stdpath.Dir(reqPath))
		if err != nil {
			if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
				return err
			}
		}
		if !common.CanWrite(meta, reqPath) {
			return errs.PermissionDenied
		}
	}
	return fs.MakeDir(ctx, reqPath)
}

func Remove(ctx context.Context, path string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	if !user.CanRemove() || !user.CanFTPManage() {
		return errs.PermissionDenied
	}
	reqPath, err := user.JoinPath(path)
	if err != nil {
		return err
	}
	if err = RemoveStage(reqPath); !errors.Is(err, errs.ObjectNotFound) {
		return err
	}
	return fs.Remove(ctx, reqPath)
}

func Rename(ctx context.Context, oldPath, newPath string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	srcPath, err := user.JoinPath(oldPath)
	if err != nil {
		return err
	}
	dstPath, err := user.JoinPath(newPath)
	if err != nil {
		return err
	}
	srcDir, srcBase := stdpath.Split(srcPath)
	dstDir, dstBase := stdpath.Split(dstPath)
	if srcDir == dstDir {
		if !user.CanRename() || !user.CanFTPManage() {
			return errs.PermissionDenied
		}
		if err = MoveStage(srcPath, dstPath); !errors.Is(err, errs.ObjectNotFound) {
			return err
		}
		return fs.Rename(ctx, srcPath, dstBase)
	} else {
		if !user.CanFTPManage() || !user.CanMove() || (srcBase != dstBase && !user.CanRename()) {
			return errs.PermissionDenied
		}
		if err = MoveStage(srcPath, dstPath); !errors.Is(err, errs.ObjectNotFound) {
			return err
		}
		if srcBase != dstBase {
			err = fs.Rename(ctx, srcPath, dstBase, true)
			if err != nil {
				return err
			}
		}
		_, err = fs.Move(ctx, stdpath.Join(srcDir, dstBase), dstDir)
		return err
	}
}
