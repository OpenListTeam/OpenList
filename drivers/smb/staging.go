package smb

import (
	"errors"
	"fmt"
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
)

// How uploads are published, learned on the first upload after Init.
const (
	publishUnknown int32 = iota
	// write a staging file and rename it over the destination
	publishStaged
	// the server cannot rename over a file: write the destination in place
	publishInPlace
	// nor overwrite one in place: only ever create new files
	publishNewOnly
)

// errCannotReplace refuses an overwrite the server would botch: Samba with
// vfs_streams_xattr on a filesystem without xattr support truncates a file
// before refusing to overwrite it.
var errCannotReplace = errors.New("the SMB server cannot replace existing files, so the existing file was left as it was")

var stagingRe = regexp.MustCompile(`^\.openlist-upload-[A-Za-z0-9]{16}$`)

func stagingName() string { return stagingPrefix + random.String(16) }

// isStaging reports whether name is an upload in progress. Only the exact
// generated form matches, so a user's own file that merely starts with the
// prefix stays visible and is never reclaimed.
func isStaging(name string) bool { return stagingRe.MatchString(name) }

// publishModeFor returns how uploads are published. The first upload after
// Init probes the server in its own directory. A server that cannot rename
// over an existing file gets in-place writes instead, as before minus any
// removal on failure; one that cannot overwrite in place either only gets
// new files.
func (d *SMB) publishModeFor(dir string) int32 {
	if mode := d.publishMode.Load(); mode != publishUnknown {
		return mode
	}
	d.probeMu.Lock()
	defer d.probeMu.Unlock()
	if mode := d.publishMode.Load(); mode != publishUnknown {
		return mode
	}
	mode, err := d.probePublish(dir)
	switch mode {
	case publishUnknown:
		// could not even create the probe files here: decide on a later upload
		return publishInPlace
	case publishInPlace:
		utils.Log.Warnf("[smb] the server cannot replace a file by renaming over it, uploads write in place: %v", err)
	case publishNewOnly:
		utils.Log.Warnf("[smb] the server can neither rename over nor overwrite an existing file, uploads only create new files: %v", err)
	}
	d.publishMode.Store(mode)
	return mode
}

// probePublish finds out how the server can replace a file, with two staging
// files in dir: hidden, and left for List to reclaim if the server keeps them.
func (d *SMB) probePublish(dir string) (int32, error) {
	src, dst := path.Join(dir, stagingName()), path.Join(dir, stagingName())
	defer func() {
		_ = d.fs.Remove(dst)
		_ = d.fs.Remove(src)
	}()
	for _, name := range []string{src, dst} {
		f, err := d.fs.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if err != nil {
			return publishUnknown, err
		}
		_ = f.Close()
	}
	renameErr := d.fs.Rename(src, dst)
	if renameErr == nil {
		return publishStaged, nil
	}
	f, err := d.fs.OpenFile(dst, os.O_RDWR|os.O_TRUNC, 0o666)
	if err != nil {
		return publishNewOnly, fmt.Errorf("%w; %w", renameErr, err)
	}
	_ = f.Close()
	return publishInPlace, renameErr
}
