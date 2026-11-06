package chunk

import (
	"context"
	stdpath "path"
	"regexp"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
)

// The parts of an upload are written to a uniquely named staging folder next
// to the chunk folder and moved into place once complete. A failed or
// canceled upload then only ever removes its own staging folder: never a
// chunk folder another upload may own by then, and never the previous
// version of a file being overwritten.
const (
	stagingPrefix = ".openlist-upload-"
	// staging folders older than this belong to an upload that can no longer
	// finish (the process was killed); List reclaims them
	staleStagingAge = 24 * time.Hour
)

var stagingRe = regexp.MustCompile(`^\.openlist-upload-[A-Za-z0-9]{16}$`)

func stagingName() string { return stagingPrefix + random.String(16) }

// isStaging reports whether name is an upload in progress. Only the exact
// generated form matches, so a user's own entry that merely starts with the
// prefix stays visible and is never reclaimed.
func isStaging(name string) bool { return stagingRe.MatchString(name) }

func isStaleStaging(obj model.Obj) bool { return time.Since(obj.ModTime()) > staleStagingAge }

func canRename(storage driver.Driver) bool {
	switch storage.(type) {
	case driver.Rename, driver.RenameResult:
		return true
	}
	return false
}

// publish moves a fully uploaded staging folder into place as name, keeping
// the previous version until the new one is complete.
func publish(ctx context.Context, remoteStorage driver.Driver, parent, staging, name string) error {
	dst := stdpath.Join(parent, name)
	// cleanup must not depend on the upload's context, which may be canceled
	rctx := context.WithoutCancel(ctx)
	backup := ""
	if _, err := op.Get(ctx, remoteStorage, dst); err == nil {
		backup = stagingName()
		if err := op.Rename(ctx, remoteStorage, dst, backup); err != nil {
			_ = op.Remove(rctx, remoteStorage, staging)
			return err
		}
	}
	if err := op.Rename(ctx, remoteStorage, staging, name); err != nil {
		if backup != "" {
			_ = op.Rename(rctx, remoteStorage, stdpath.Join(parent, backup), name)
		}
		_ = op.Remove(rctx, remoteStorage, staging)
		return err
	}
	if backup != "" {
		_ = op.Remove(rctx, remoteStorage, stdpath.Join(parent, backup))
	}
	return nil
}
