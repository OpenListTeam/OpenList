package chunk

import (
	"context"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// The parts of an upload are written to a staging folder next to the chunk
// folder (see driver.StagingName) and moved into place once complete. A failed
// or canceled upload then only ever removes its own staging folder: never a
// chunk folder another upload may own by then, and never the previous version
// of a file being overwritten.

func canRename(storage driver.Driver) bool {
	switch storage.(type) {
	case driver.Rename, driver.RenameResult:
		return true
	}
	return false
}

// publish moves a fully uploaded staging folder into place as name, keeping
// the previous version until the new one is complete.
func (d *Chunk) publish(ctx context.Context, remoteStorage driver.Driver, parent, staging, name string) error {
	dst := stdpath.Join(parent, name)
	// cleanup must not depend on the upload's context, which may be canceled
	rctx := context.WithoutCancel(ctx)
	backup := ""
	if _, err := op.Get(ctx, remoteStorage, dst); err == nil {
		backup = driver.StagingName(d.GetStorage())
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
