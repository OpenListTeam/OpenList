package chunk

import (
	"context"
	"errors"
	"fmt"
	stdpath "path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
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

// Chunk.folderRename
const (
	folderRenameUnknown int32 = iota
	folderRenameWorks
	folderRenameRefused
)

// stage returns a staging folder for an upload into parent, kept out of
// listings until release is called, or "" when the remote cannot move a folder
// into place, so the upload has to be written in place. A remote may implement
// Rename and still refuse folders (Lanzou): the first staged upload finds out
// with a folder of its own, creating its staging folder and moving it to a
// second staging name, which it then uses.
func (d *Chunk) stage(ctx context.Context, remoteStorage driver.Driver, parent string) (staging string, release func(), err error) {
	if !canRename(remoteStorage) || d.folderRename.Load() == folderRenameRefused {
		return "", nil, nil
	}
	staging = stdpath.Join(parent, driver.StagingName(d.GetStorage()))
	release = d.staging.Hold(staging)
	if d.folderRename.Load() == folderRenameWorks {
		return staging, release, nil
	}
	if err := op.MakeDir(ctx, remoteStorage, staging); err != nil {
		release()
		return "", nil, err
	}
	movedName := driver.StagingName(d.GetStorage())
	moved := stdpath.Join(parent, movedName)
	releaseMoved := d.staging.Hold(moved)
	err = renameOnRemote(ctx, remoteStorage, staging, movedName)
	if err == nil {
		d.folderRename.Store(folderRenameWorks)
		release()
		return moved, releaseMoved, nil
	}
	if errors.Is(err, errs.NotSupport) || errors.Is(err, errs.NotImplement) {
		d.folderRename.Store(folderRenameRefused)
		err = nil
	}
	rctx := context.WithoutCancel(ctx)
	_ = op.Remove(rctx, remoteStorage, staging)
	_ = op.Remove(rctx, remoteStorage, moved)
	release()
	releaseMoved()
	return "", nil, err
}

// putInPlace writes the parts of file straight into its chunk folder, on a
// remote that cannot move a staging folder into place. Uploads of the file
// take turns, so nothing else writes the folder meanwhile. A failed upload
// removes the folder: some parts of the previous version may be replaced by
// then, and what is left must list neither as a mix of both versions nor as
// the start of the new one. A successful one removes what is left of the
// previous version.
func (d *Chunk) putInPlace(ctx context.Context, remoteStorage driver.Driver, parent, name string, file model.FileStreamer, up driver.UpdateProgress) error {
	dir := stdpath.Join(parent, name)
	unlock, err := d.publishing.Lock(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	if _, err := d.previousChunked(ctx, remoteStorage, parent, name); err != nil {
		return err
	}
	if err := d.putParts(ctx, remoteStorage, dir, file, up); err != nil {
		_ = op.Remove(context.WithoutCancel(ctx), remoteStorage, dir)
		return err
	}
	if err := d.dropStale(ctx, remoteStorage, dir, file); err != nil {
		return err
	}
	return d.dropOtherForm(ctx, remoteStorage, parent, file.GetName(), false)
}

// dropStale removes what a previous version left in the chunk folder dir that
// file, just written into it in place, did not replace: parts past its last
// one, and hash markers of other content.
func (d *Chunk) dropStale(ctx context.Context, remoteStorage driver.Driver, dir string, file model.FileStreamer) error {
	rctx := context.WithoutCancel(ctx)
	objs, err := op.ListFresh(rctx, remoteStorage, dir)
	if err != nil {
		return err
	}
	parts := d.partCount(file.GetSize())
	markers := make(map[string]bool)
	if d.StoreHash {
		for ht, value := range file.GetHash().All() {
			markers[d.hashMarkerName(ht, value)] = true
		}
	}
	for _, o := range objs {
		if o.IsDir() {
			continue
		}
		var stale bool
		if strings.HasPrefix(o.GetName(), "hash_") {
			stale = !markers[o.GetName()]
		} else if idx, err := strconv.Atoi(strings.TrimSuffix(o.GetName(), d.CustomExt)); err == nil {
			stale = idx >= parts
		}
		if !stale {
			continue
		}
		if err := op.Remove(rctx, remoteStorage, stdpath.Join(dir, o.GetName())); err != nil {
			return fmt.Errorf("uploaded, but %s of the previous version could not be removed: %w", o.GetName(), err)
		}
	}
	return nil
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

// isChunkedFile reports whether obj, found at path on the remote, is a folder
// that List shows as a chunked file: part 0 and every part before the last one
// present. Any other entry of that name belongs to the user.
func (d *Chunk) isChunkedFile(ctx context.Context, remoteStorage driver.Driver, obj model.Obj, path string) (bool, error) {
	if !obj.IsDir() {
		return false, nil
	}
	objs, err := op.ListFresh(ctx, remoteStorage, path)
	if err != nil {
		return false, err
	}
	sizes := make(map[int]int64)
	last := -1
	for _, o := range objs {
		if o.IsDir() || strings.HasPrefix(o.GetName(), "hash_") {
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSuffix(o.GetName(), d.CustomExt))
		if err != nil || idx < 0 {
			continue
		}
		sizes[idx] = o.GetSize()
		last = max(last, idx)
	}
	if _, ok := sizes[0]; !ok {
		return false, nil
	}
	for i := 1; i < last; i++ {
		if sizes[i] == 0 {
			return false, nil
		}
	}
	return true, nil
}

// previousChunked tells what the remote holds at a chunk folder's name:
// nothing (false), or the previous version as a chunked file (true). Anything
// else there is the user's own and refuses the upload, untouched.
func (d *Chunk) previousChunked(ctx context.Context, remoteStorage driver.Driver, parent, name string) (bool, error) {
	obj, err := lookup(ctx, remoteStorage, parent, name)
	if err != nil || obj == nil {
		return false, err
	}
	ok, err := d.isChunkedFile(ctx, remoteStorage, obj, stdpath.Join(parent, name))
	if err != nil {
		return false, err
	}
	if !ok {
		return false, fmt.Errorf("the remote holds %s, which is not a chunked file, so it was left as it was", name)
	}
	return true, nil
}

// dropOtherForm removes the previous version of a file that a successful upload
// stored the other way, across ChunkLargeFileOnly's threshold: whole while the
// new one is chunked (folder false), or chunked while the new one is whole
// (folder true). Left in place, the file would be listed twice, and Get
// prefers the whole one. Only a whole file, or a folder holding a chunked
// file, is that form. The caller holds the destination's publish lock.
func (d *Chunk) dropOtherForm(ctx context.Context, remoteStorage driver.Driver, parent, other string, folder bool) error {
	rctx := context.WithoutCancel(ctx)
	obj, err := lookup(rctx, remoteStorage, parent, other)
	if err != nil || obj == nil || obj.IsDir() != folder {
		return err
	}
	if folder {
		if ok, err := d.isChunkedFile(rctx, remoteStorage, obj, stdpath.Join(parent, other)); err != nil || !ok {
			return err
		}
	}
	if err := op.Remove(rctx, remoteStorage, stdpath.Join(parent, other)); err != nil {
		return fmt.Errorf("uploaded, but the previous version stored as %s could not be removed: %w", other, err)
	}
	return nil
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
	had, err := d.previousChunked(ctx, remoteStorage, parent, name)
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
	return d.dropOtherForm(ctx, remoteStorage, parent, strings.TrimPrefix(name, d.ChunkPrefix), false)
}
