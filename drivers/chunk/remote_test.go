package chunk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// Remotes that cannot move a staging folder into place.
const (
	noFolderRenameDriver = "LocalNoFolderRename"
	noRenameDriver       = "LocalNoRename"
)

func registerTestRemotes() {
	op.RegisterDriver(func() driver.Driver { return &localNoFolderRename{} })
	op.RegisterDriver(func() driver.Driver { return newLocalNoRename() })
}

// localNoFolderRename is Local refusing to rename folders, as Lanzou does.
type localNoFolderRename struct{ local.Local }

func (d *localNoFolderRename) Config() driver.Config {
	c := d.Local.Config()
	c.Name = noFolderRenameDriver
	return c
}

func (d *localNoFolderRename) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if srcObj.IsDir() {
		return errs.NotSupport
	}
	return d.Local.Rename(ctx, srcObj, newName)
}

// localNoRename is Local without Rename.
type localNoRename struct {
	driver.Driver
	l *local.Local
}

func newLocalNoRename() *localNoRename {
	l := &local.Local{}
	return &localNoRename{Driver: l, l: l}
}

func (d *localNoRename) Config() driver.Config {
	c := d.l.Config()
	c.Name = noRenameDriver
	return c
}

func (d *localNoRename) GetRootPath() string { return d.l.GetRootPath() }
func (d *localNoRename) Get(ctx context.Context, path string) (model.Obj, error) {
	return d.l.Get(ctx, path)
}
func (d *localNoRename) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return d.l.MakeDir(ctx, parentDir, dirName)
}
func (d *localNoRename) Remove(ctx context.Context, obj model.Obj) error { return d.l.Remove(ctx, obj) }
func (d *localNoRename) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	return d.l.Put(ctx, dstDir, file, up)
}

// putHashed uploads data as name with its md5, which the chunk storage keeps
// next to the parts.
func putHashed(t *testing.T, d driver.Driver, name string, data []byte) {
	t.Helper()
	err := op.Put(context.Background(), d, "/", &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     int64(len(data)),
			Modified: time.Now(),
			HashInfo: utils.NewHashInfo(utils.MD5, utils.HashData(utils.MD5, data)),
		},
		Reader: bytes.NewReader(data),
	}, nil)
	if err != nil {
		t.Fatalf("upload %s: %v", name, err)
	}
}

// A remote may implement Rename and still refuse to rename folders (Lanzou).
// Uploads then write their parts in place, instead of staging them only to
// fail moving the staging folder into place.
func TestChunkUploadsWhereFoldersCannotBeRenamed(t *testing.T) {
	d, root := newChunkStorageOver(t, noFolderRenameDriver, "")
	for _, b := range []byte{'a', 'b'} {
		if err := putFile(context.Background(), d, "f.bin", bytes.NewReader(content(3000, b)), 3000); err != nil {
			t.Fatalf("upload of %c: %v", b, err)
		}
		if got := readChunked(t, root, "f.bin"); !bytes.Equal(got, content(3000, b)) {
			t.Fatalf("after the upload of %c the file holds %d bytes of other content", b, len(got))
		}
	}
	if left := stagingLeftovers(t, root); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

// Written in place, a shorter version leaves nothing of the longer one it
// replaces: neither parts past its own nor the old content's hash markers.
func TestChunkInPlaceOverwriteLeavesNothingOfThePreviousVersion(t *testing.T) {
	for _, remote := range []string{noRenameDriver, noFolderRenameDriver} {
		t.Run(remote, func(t *testing.T) {
			d, root := newChunkStorageOver(t, remote, `"store_hash":true`)
			body := content(1500, 'b')
			putHashed(t, d, "f.bin", content(5000, 'a'))
			putHashed(t, d, "f.bin", body)
			if got := readChunked(t, root, "f.bin"); !bytes.Equal(got, body) {
				t.Fatalf("the file holds %d bytes, want the 1500 just written", len(got))
			}
			obj, err := op.Get(context.Background(), d, "/f.bin")
			if err != nil {
				t.Fatal(err)
			}
			if md5 := obj.GetHash().GetHash(utils.MD5); obj.GetSize() != 1500 || md5 != utils.HashData(utils.MD5, body) {
				t.Fatalf("Get = %d bytes with md5 %s, want 1500 with the new md5", obj.GetSize(), md5)
			}
			markers, err := filepath.Glob(filepath.Join(root, "[[]openlist_chunk]f.bin", "hash_*"))
			if want := "hash_md5_" + utils.HashData(utils.MD5, body); err != nil || len(markers) != 1 || filepath.Base(markers[0]) != want {
				t.Fatalf("hash markers = %v (%v), want only %s", markers, err, want)
			}
		})
	}
}

// Written in place, a failed upload leaves no file behind: its parts replaced
// some of the previous version's, and what is left must list neither as a mix
// of both versions nor as the start of the new one.
func TestChunkInPlaceFailedUploadLeavesNoFile(t *testing.T) {
	for _, remote := range []string{noRenameDriver, noFolderRenameDriver} {
		for _, previous := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/previous=%v", remote, previous), func(t *testing.T) {
				d, _ := newChunkStorageOver(t, remote, "")
				if previous {
					putHashed(t, d, "f.bin", content(5000, 'a'))
				}
				boom := errors.New("client went away")
				r := &testReader{data: content(5000, 'b'), failAt: 2500, failErr: boom}
				if err := putFile(context.Background(), d, "f.bin", r, 5000); !errors.Is(err, boom) {
					t.Fatalf("upload = %v, want %v", err, boom)
				}
				if names := listNames(t, d); len(names) != 0 {
					t.Fatalf("listing = %v, want nothing", names)
				}
			})
		}
	}
}
