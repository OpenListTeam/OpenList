package smb

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

// Live checks against a writable SMB share, e.g. a Samba container:
//
//	OPENLIST_SMB_INTEGRATION=1 OPENLIST_SMB_ADDRESS=127.0.0.1:1445 \
//	OPENLIST_SMB_USERNAME=tester OPENLIST_SMB_PASSWORD=... OPENLIST_SMB_SHARE=share \
//	go test ./drivers/smb -run TestIntegration
func integrationDriver(t *testing.T) (*SMB, string) {
	t.Helper()
	if os.Getenv("OPENLIST_SMB_INTEGRATION") != "1" {
		t.Skip("set OPENLIST_SMB_INTEGRATION=1 to run live SMB checks")
	}
	d := &SMB{Addition: Addition{
		RootPath:  driver.RootPath{RootFolderPath: "."},
		Address:   os.Getenv("OPENLIST_SMB_ADDRESS"),
		Username:  os.Getenv("OPENLIST_SMB_USERNAME"),
		Password:  os.Getenv("OPENLIST_SMB_PASSWORD"),
		ShareName: os.Getenv("OPENLIST_SMB_SHARE"),
	}}
	d.SetStorage(model.Storage{MountPath: fmt.Sprintf("/smb-it-%d", time.Now().UnixNano())})
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	dir := fmt.Sprintf("openlist-it-%d", time.Now().UnixNano())
	if err := d.fs.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	t.Cleanup(func() {
		_ = d.fs.RemoveAll(dir)
		_ = d.Drop(context.Background())
	})
	return d, dir
}

// pausingReader serves data, optionally pausing once at pauseAt until
// released, and fails once at failAt when failErr is set. Once ctxDone is
// closed it yields no more data, like a multipart window closed by an abort.
type pausingReader struct {
	data     []byte
	pos      int
	pauseAt  int
	paused   chan struct{}
	release  chan struct{}
	didPause bool
	ctxDone  <-chan struct{}
	failAt   int
	failErr  error
}

func (r *pausingReader) Read(p []byte) (int, error) {
	if r.paused != nil && !r.didPause && r.pos >= r.pauseAt {
		r.didPause = true
		close(r.paused)
		<-r.release
	}
	if r.ctxDone != nil {
		select {
		case <-r.ctxDone:
			return 0, context.Canceled
		default:
		}
	}
	if r.failErr != nil && r.pos >= r.failAt {
		return 0, r.failErr
	}
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	end := len(r.data)
	if r.paused != nil && !r.didPause && r.pauseAt > r.pos && r.pauseAt < end {
		end = r.pauseAt
	}
	if r.failErr != nil && r.failAt > r.pos && r.failAt < end {
		end = r.failAt
	}
	n := copy(p, r.data[r.pos:end])
	r.pos += n
	return n, nil
}

func pausing(data []byte, at int) *pausingReader {
	return &pausingReader{data: data, pauseAt: at, paused: make(chan struct{}), release: make(chan struct{})}
}

func fileStream(name string, r io.Reader, size int64) *stream.FileStream {
	return &stream.FileStream{Obj: &model.Object{Name: name, Size: size, Modified: time.Now()}, Reader: r}
}

func readFile(t *testing.T, d *SMB, p string) ([]byte, error) {
	t.Helper()
	f, err := d.fs.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func dirEntries(t *testing.T, d *SMB, dir string) []string {
	t.Helper()
	infos, err := d.fs.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %s: %v", dir, err)
	}
	var names []string
	for _, fi := range infos {
		names = append(names, fi.Name())
	}
	return names
}

func TestIntegrationUploadsNewFile(t *testing.T) {
	d, dir := integrationDriver(t)
	body := []byte("a new file")
	if err := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("new.bin", bytes.NewReader(body), int64(len(body))), func(float64) {}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, err := readFile(t, d, path.Join(dir, "new.bin")); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("read back %q, %v", got, err)
	}
	// a server that cannot remove files keeps the probe files, hidden
	objs, err := d.List(context.Background(), &model.Object{Path: dir}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 1 || objs[0].GetName() != "new.bin" {
		t.Fatalf("listing shows %d entries, want only new.bin", len(objs))
	}
}

// #3178 on SMB: an abandoned upload that had written some data is reaped
// after a later upload of the same name completed; the later file survives.
func TestIntegrationCanceledUploadKeepsLaterUpload(t *testing.T) {
	d, dir := integrationDriver(t)
	stale := pausing(bytes.Repeat([]byte("s"), 1<<20), 64<<10)
	ctx, cancel := context.WithCancel(context.Background())
	stale.ctxDone = ctx.Done()
	errc := make(chan error, 1)
	go func() {
		errc <- d.Put(ctx, &model.Object{Path: dir}, fileStream("f.bin", stale, 1<<20), func(float64) {})
	}()
	<-stale.paused
	later := bytes.Repeat([]byte("w"), 200<<10)
	if err := op.Put(context.Background(), d, "/"+dir, fileStream("f.bin", bytes.NewReader(later), int64(len(later))), nil); err != nil {
		t.Fatalf("later upload: %v", err)
	}
	cancel() // session GC, abort or supersede
	close(stale.release)
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("reaped upload returned %v, want context.Canceled", err)
	}
	got, err := readFile(t, d, path.Join(dir, "f.bin"))
	if err != nil {
		t.Fatalf("the later, completed upload was deleted: %v", err)
	}
	if !bytes.Equal(got, later) {
		t.Fatalf("the later upload was damaged (len %d, want %d)", len(got), len(later))
	}
	if names := dirEntries(t, d, dir); len(names) != 1 {
		t.Fatalf("directory holds %v after the reap, want only f.bin", names)
	}
}

// An abandoned upload that has written nothing yet must not hold the name:
// a later upload of the same file goes through.
func TestIntegrationIdleUploadDoesNotBlockLaterUpload(t *testing.T) {
	d, dir := integrationDriver(t)
	stale := pausing(bytes.Repeat([]byte("s"), 1<<20), 0)
	ctx, cancel := context.WithCancel(context.Background())
	stale.ctxDone = ctx.Done()
	errc := make(chan error, 1)
	go func() {
		errc <- d.Put(ctx, &model.Object{Path: dir}, fileStream("f.bin", stale, 1<<20), func(float64) {})
	}()
	<-stale.paused
	later := bytes.Repeat([]byte("w"), 200<<10)
	err := op.Put(context.Background(), d, "/"+dir, fileStream("f.bin", bytes.NewReader(later), int64(len(later))), nil)
	cancel()
	close(stale.release)
	<-errc
	if err != nil {
		t.Fatalf("a later upload of the same name was blocked by the idle one: %v", err)
	}
	if got, err := readFile(t, d, path.Join(dir, "f.bin")); err != nil || !bytes.Equal(got, later) {
		t.Fatalf("later upload not intact: len %d, %v", len(got), err)
	}
}

// A failed overwrite leaves the previous version intact.
func TestIntegrationFailedOverwriteKeepsPrevious(t *testing.T) {
	d, dir := integrationDriver(t)
	old := []byte("previous version")
	if err := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("f.bin", bytes.NewReader(old), int64(len(old))), func(float64) {}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	boom := errors.New("client went away")
	r := &pausingReader{data: bytes.Repeat([]byte("n"), 1<<20), failAt: 64 << 10, failErr: boom}
	if err := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("f.bin", r, 1<<20), func(float64) {}); !errors.Is(err, boom) {
		t.Fatalf("overwrite returned %v, want %v", err, boom)
	}
	if got, err := readFile(t, d, path.Join(dir, "f.bin")); err != nil || !bytes.Equal(got, old) {
		t.Fatalf("previous version destroyed by a failed overwrite: len %d, %v", len(got), err)
	}
	if names := dirEntries(t, d, dir); len(names) != 1 {
		t.Fatalf("directory holds %v after the failure, want only f.bin", names)
	}
}

// Staging files never show up in listings, stale ones are reclaimed, and a
// user's look-alike file is left alone.
func TestIntegrationListHidesAndReapsStaging(t *testing.T) {
	d, dir := integrationDriver(t)
	fresh, stale := path.Join(dir, stagingName()), path.Join(dir, stagingName())
	lookalike := path.Join(dir, ".openlist-upload-notes.txt")
	for _, p := range []string{fresh, stale, lookalike} {
		f, err := d.fs.Create(p)
		if err != nil {
			t.Fatalf("create %s: %v", p, err)
		}
		_ = f.Close()
	}
	past := time.Now().Add(-staleStagingAge - time.Hour)
	if err := d.fs.Chtimes(stale, past, past); err != nil {
		t.Fatalf("age %s: %v", stale, err)
	}
	objs, err := d.List(context.Background(), &model.Object{Path: dir}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var listed []string
	for _, o := range objs {
		listed = append(listed, o.GetName())
	}
	if len(listed) != 1 || listed[0] != ".openlist-upload-notes.txt" {
		t.Fatalf("listing = %v, want only the user's look-alike file", listed)
	}
	if _, err := d.fs.Stat(fresh); err != nil {
		t.Fatalf("a running upload's staging file was reclaimed: %v", err)
	}
	if _, err := d.fs.Stat(stale); err == nil {
		t.Fatal("a stale staging file was not reclaimed")
	}
}

// Replacing a destination that another client holds open must leave a
// consistent state either way: the new content (the server allowed the
// replace) or the old one (it refused), and never a leftover staging file.
func TestIntegrationOpenDestinationFailsCleanly(t *testing.T) {
	d, dir := integrationDriver(t)
	dst := path.Join(dir, "f.bin")
	old := []byte("old content")
	if err := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("f.bin", bytes.NewReader(old), int64(len(old))), func(float64) {}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	reader, err := d.fs.Open(dst)
	if err != nil {
		t.Fatalf("open %s: %v", dst, err)
	}
	body := []byte("new content")
	putErr := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("f.bin", bytes.NewReader(body), int64(len(body))), func(float64) {})
	_ = reader.Close()
	got, err := readFile(t, d, dst)
	if err != nil {
		t.Fatalf("destination gone: %v", err)
	}
	want := body
	if putErr != nil {
		t.Logf("server refused to replace an open file: %v", putErr)
		want = old
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination = %q, want %q (put err %v)", got, want, putErr)
	}
	for _, name := range dirEntries(t, d, dir) {
		if strings.HasPrefix(name, stagingPrefix) {
			t.Fatalf("staging file %s left behind (put err %v)", name, putErr)
		}
	}
}

// Whatever the server supports, an overwrite either replaces the file or
// leaves the previous version as it was. Samba with vfs_streams_xattr on a
// filesystem without xattr support truncates a file before refusing to
// overwrite it, so such a server must not be asked to overwrite at all.
func TestIntegrationOverwriteNeverDestroysPrevious(t *testing.T) {
	d, dir := integrationDriver(t)
	old := []byte("previous version")
	if err := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("f.bin", bytes.NewReader(old), int64(len(old))), func(float64) {}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := []byte("new version, longer than the previous one")
	putErr := d.Put(context.Background(), &model.Object{Path: dir}, fileStream("f.bin", bytes.NewReader(body), int64(len(body))), func(float64) {})
	got, err := readFile(t, d, path.Join(dir, "f.bin"))
	if err != nil {
		t.Fatalf("destination gone: %v", err)
	}
	want := body
	if putErr != nil {
		t.Logf("server cannot replace files: %v", putErr)
		want = old
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination = %q, want %q (put err %v)", got, want, putErr)
	}
}
