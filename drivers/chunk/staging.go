package chunk

import (
	"context"
	"fmt"
	stdpath "path"
	"strings"
	"unicode/utf8"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
)

// The parts of an upload are written to a staging folder next to the chunk
// folder (see driver.StagingName) and moved into place once complete. A failed
// or canceled upload then only ever removes its own staging folder: never a
// chunk folder another upload may own by then, and never the previous version
// of a file being overwritten.

// backupInfix names the previous version of a file while its replacement
// moves into place. The backup lists as a file of its own, so should it have
// to stay, the user finds it next to the original.
const backupInfix = ".openlist-backup-"

// renameOnRemote is op.Rename; tests make it fail
var renameOnRemote = op.Rename

func canRename(storage driver.Driver) bool {
	switch storage.(type) {
	case driver.Rename, driver.RenameResult:
		return true
	}
	return false
}

// backupName names the previous version of name while its replacement moves
// in. It lists as a file next to the original, and stays within the 255 bytes
// most filesystems allow for a name.
func backupName(name string) string {
	suffix := backupInfix + random.String(8)
	if limit := 255 - len(suffix); len(name) > limit {
		for limit > 0 && !utf8.RuneStart(name[limit]) {
			limit--
		}
		name = name[:limit]
	}
	return name + suffix
}

// lookup returns what dir on the remote holds as name now, or nil. A remote
// that finds objects by path itself is asked as well: it may take another
// name for this one, such as one differing only in case.
func lookup(ctx context.Context, remoteStorage driver.Driver, dir, name string) (model.Obj, error) {
	objs, err := op.ListFresh(ctx, remoteStorage, dir)
	if err != nil {
		return nil, err
	}
	for _, obj := range objs {
		if obj.GetName() == name {
			return obj, nil
		}
	}
	if g, ok := remoteStorage.(driver.Getter); ok {
		if obj, err := g.Get(ctx, stdpath.Join(dir, name)); err == nil {
			return obj, nil
		}
	}
	return nil, nil
}

func present(ctx context.Context, remoteStorage driver.Driver, dir, name string) (bool, error) {
	obj, err := lookup(ctx, remoteStorage, dir, name)
	return obj != nil, err
}

// publish moves a fully uploaded staging folder into place as name, keeping the
// previous version apart until the new one is in. A remote may rename by
// copying part by part, so a failed move can leave either side half done:
// the previous version is never moved back over anything at the destination,
// and whenever it may still sit under its backup name, the error says so.
// Publishes of one destination run one at a time.
func (d *Chunk) publish(ctx context.Context, remoteStorage driver.Driver, parent, staging, name string) error {
	dst := stdpath.Join(parent, name)
	// cleanup must not depend on the upload's context, which may be canceled
	rctx := context.WithoutCancel(ctx)
	// an upload canceled while it waits publishes nothing
	unlock, err := d.publishing.Lock(ctx, dst)
	if err != nil {
		_ = op.Remove(rctx, remoteStorage, staging)
		return err
	}
	defer unlock()
	had, err := present(ctx, remoteStorage, parent, name)
	if err != nil {
		_ = op.Remove(rctx, remoteStorage, staging)
		return err
	}
	backup := ""
	if had {
		backup = backupName(name)
		// out of listings until it is either gone or all that is left
		release := d.staging.Hold(stdpath.Join(parent, backup))
		defer release()
		if err := renameOnRemote(ctx, remoteStorage, dst, backup); err != nil {
			_ = op.Remove(rctx, remoteStorage, staging)
			if kept, _ := present(rctx, remoteStorage, parent, backup); kept {
				return fmt.Errorf("%w; the previous version may also be kept as %s", err, strings.TrimPrefix(backup, d.ChunkPrefix))
			}
			return err
		}
	}
	if err := renameOnRemote(ctx, remoteStorage, staging, name); err != nil {
		_ = op.Remove(rctx, remoteStorage, staging)
		if backup == "" {
			return err
		}
		kept := strings.TrimPrefix(backup, d.ChunkPrefix)
		// only an empty destination gets the previous version back: the new
		// one may have got halfway in
		if taken, perr := present(rctx, remoteStorage, parent, name); perr != nil || taken {
			return fmt.Errorf("%w; the previous version was kept as %s", err, kept)
		}
		if rerr := renameOnRemote(rctx, remoteStorage, stdpath.Join(parent, backup), name); rerr != nil {
			return fmt.Errorf("%w; the previous version could not be moved back (%v) and was kept as %s", err, rerr, kept)
		}
		return err
	}
	if backup != "" {
		if err := op.Remove(rctx, remoteStorage, stdpath.Join(parent, backup)); err != nil {
			utils.Log.Warnf("[chunk] uploaded %s, but the previous version is left as %s: %v", dst, backup, err)
		}
	}
	return nil
}
