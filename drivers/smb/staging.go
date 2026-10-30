package smb

import (
	"os"
	"path"
	"regexp"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
)

// An upload is written to a uniquely named staging file next to its
// destination and renamed over it once complete. A canceled or failed upload
// then only ever removes its own staging file: never a destination another
// upload may own by then, and never the previous version of a file being
// overwritten.
const (
	stagingPrefix = ".openlist-upload-"
	// staging files older than this belong to an upload that can no longer
	// finish (the process was killed); List reclaims them
	staleStagingAge = 24 * time.Hour
	// fixed probe names in the staging form: hidden like any staging file,
	// and reused, so a server that cannot remove them does not collect more
	probeSource = stagingPrefix + "probe00000000000"
	probeTarget = stagingPrefix + "probe00000000001"
)

// How uploads are published, learned on the first upload after Init.
const (
	publishUnknown int32 = iota
	publishStaged
	publishInPlace
)

var stagingRe = regexp.MustCompile(`^\.openlist-upload-[A-Za-z0-9]{16}$`)

func stagingName() string { return stagingPrefix + random.String(16) }

// isStaging reports whether name is an upload in progress. Only the exact
// generated form matches, so a user's own file that merely starts with the
// prefix stays visible and is never reclaimed.
func isStaging(name string) bool { return stagingRe.MatchString(name) }

// stagedUploads reports whether uploads can be staged and published by a
// rename over the destination. The first upload after Init probes the
// server in its own directory; a server that cannot rename over an existing
// file (e.g. Samba with vfs_streams_xattr on a filesystem without xattrs)
// gets in-place writes instead, as before, minus any removal on failure.
func (d *SMB) stagedUploads(dir string) bool {
	if mode := d.publishMode.Load(); mode != publishUnknown {
		return mode == publishStaged
	}
	d.probeMu.Lock()
	defer d.probeMu.Unlock()
	if mode := d.publishMode.Load(); mode != publishUnknown {
		return mode == publishStaged
	}
	mode := publishStaged
	if err := d.probeAtomicPublish(dir); err != nil {
		utils.Log.Warnf("[smb] the server cannot replace a file by renaming over it, uploads write in place: %v", err)
		mode = publishInPlace
	}
	d.publishMode.Store(mode)
	return mode == publishStaged
}

func (d *SMB) probeAtomicPublish(dir string) error {
	src, dst := path.Join(dir, probeSource), path.Join(dir, probeTarget)
	for _, name := range []string{src, dst} {
		f, err := d.fs.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o666)
		if err != nil {
			_ = d.fs.Remove(src)
			return err
		}
		_ = f.Close()
	}
	err := d.fs.Rename(src, dst)
	_ = d.fs.Remove(dst)
	_ = d.fs.Remove(src)
	return err
}
