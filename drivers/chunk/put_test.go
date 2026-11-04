package chunk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "openlist-chunk-test")
	if err != nil {
		panic(err)
	}
	dB, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic("failed to connect database")
	}
	conf.Conf = conf.DefaultConfig(dir)
	db.Init(dB)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const testPartSize = 1024

var storageSeq atomic.Int64

// newChunkStorage mounts a Local storage on a fresh directory and a Chunk
// storage on top of it, and returns the chunk driver and the local root.
func newChunkStorage(t *testing.T) (driver.Driver, string) {
	t.Helper()
	n := storageSeq.Add(1)
	root := t.TempDir()
	localMount, chunkMount := fmt.Sprintf("/chunk-remote-%d", n), fmt.Sprintf("/chunk-%d", n)
	ctx := context.Background()
	localID, err := op.CreateStorage(ctx, model.Storage{
		Driver:    "Local",
		MountPath: localMount,
		Addition:  fmt.Sprintf(`{"root_folder_path":%q,"show_hidden":true}`, root),
	})
	if err != nil {
		t.Fatalf("create local storage: %v", err)
	}
	chunkID, err := op.CreateStorage(ctx, model.Storage{
		Driver:    "Chunk",
		MountPath: chunkMount,
		Addition:  fmt.Sprintf(`{"remote_path":%q,"part_size":%d,"show_hidden":true,"num_list_workers":2}`, localMount, testPartSize),
	})
	if err != nil {
		t.Fatalf("create chunk storage: %v", err)
	}
	t.Cleanup(func() {
		_ = op.DeleteStorageById(ctx, chunkID)
		_ = op.DeleteStorageById(ctx, localID)
	})
	d, err := op.GetStorageByMountPath(chunkMount)
	if err != nil {
		t.Fatal(err)
	}
	return d, root
}

func putFile(ctx context.Context, d driver.Driver, name string, r io.Reader, size int64) error {
	return op.Put(ctx, d, "/", &stream.FileStream{
		Obj:    &model.Object{Name: name, Size: size, Modified: time.Now()},
		Reader: r,
	}, nil)
}

// readChunked concatenates the part files of a chunked file on the remote.
func readChunked(t *testing.T, root, name string) []byte {
	t.Helper()
	dir := filepath.Join(root, "[openlist_chunk]"+name)
	var out []byte
	for i := 0; ; i++ {
		b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(i)))
		if errors.Is(err, fs.ErrNotExist) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b...)
	}
}

func stagingLeftovers(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".openlist-upload-") {
			names = append(names, e.Name())
		}
	}
	return names
}

func content(n int, b byte) []byte { return bytes.Repeat([]byte{b}, n) }

// testReader serves data, optionally pausing once at pauseAt until released,
// and fails once at failAt when failErr is set. Once ctxDone is closed it
// yields no more data, like a multipart window closed by an abort.
type testReader struct {
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

func (r *testReader) Read(p []byte) (int, error) {
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

// A failed overwrite leaves the previous version intact: the failure cleanup
// used to remove the whole chunk folder, old parts included.
func TestChunkFailedOverwriteKeepsPrevious(t *testing.T) {
	d, root := newChunkStorage(t)
	ctx := context.Background()
	old := content(3000, 'a')
	if err := putFile(ctx, d, "f.bin", bytes.NewReader(old), int64(len(old))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	boom := errors.New("client went away")
	r := &testReader{data: content(3000, 'b'), failAt: 1500, failErr: boom}
	if err := putFile(ctx, d, "f.bin", r, 3000); !errors.Is(err, boom) {
		t.Fatalf("overwrite returned %v, want %v", err, boom)
	}
	if got := readChunked(t, root, "f.bin"); !bytes.Equal(got, old) {
		t.Fatalf("previous version destroyed by a failed overwrite (len %d)", len(got))
	}
	if left := stagingLeftovers(t, root); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

// #3178 on chunk: a canceled upload must not remove the chunk folder that a
// later upload of the same name completed meanwhile.
func TestChunkCanceledUploadKeepsLaterUpload(t *testing.T) {
	d, root := newChunkStorage(t)
	stale := &testReader{data: content(3000, 's'), pauseAt: 1500, paused: make(chan struct{}), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	stale.ctxDone = ctx.Done()
	errc := make(chan error, 1)
	go func() { errc <- putFile(ctx, d, "f.bin", stale, 3000) }()
	<-stale.paused
	later := content(2500, 'w')
	if err := putFile(context.Background(), d, "f.bin", bytes.NewReader(later), int64(len(later))); err != nil {
		t.Fatalf("later upload: %v", err)
	}
	cancel()
	close(stale.release)
	if err := <-errc; err == nil {
		t.Fatal("the canceled upload reported success")
	}
	if got := readChunked(t, root, "f.bin"); !bytes.Equal(got, later) {
		t.Fatalf("the later, completed upload was lost or damaged (len %d, want %d)", len(got), len(later))
	}
	if left := stagingLeftovers(t, root); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

// Overwriting with a smaller file must not leave the old file's extra parts
// behind: the size is the sum of all parts, so they corrupt the new file.
func TestChunkOverwriteWithFewerPartsLeavesNoStaleParts(t *testing.T) {
	d, root := newChunkStorage(t)
	ctx := context.Background()
	if err := putFile(ctx, d, "f.bin", bytes.NewReader(content(5000, 'a')), 5000); err != nil {
		t.Fatalf("seed: %v", err)
	}
	smaller := content(1500, 'b')
	if err := putFile(ctx, d, "f.bin", bytes.NewReader(smaller), int64(len(smaller))); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	if got := readChunked(t, root, "f.bin"); !bytes.Equal(got, smaller) {
		t.Fatalf("overwritten file reads %d bytes, want %d", len(got), len(smaller))
	}
	obj, err := op.Get(ctx, d, "/f.bin")
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetSize() != int64(len(smaller)) {
		t.Fatalf("size = %d, want %d", obj.GetSize(), len(smaller))
	}
}

func listNames(t *testing.T, d driver.Driver) []string {
	t.Helper()
	objs, err := op.List(context.Background(), d, "/", model.ListArgs{Refresh: true})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	sort.Strings(names)
	return names
}

// Staging folders never show up in listings, and the ones an upload left
// behind long ago are reclaimed. One that started long ago but is still being
// written stays, and so does a new one on a remote whose folder times never
// change. What this storage did not stage stays listed: another storage's
// staging folder, and a file, which this storage never stages.
func TestChunkListHidesStaging(t *testing.T) {
	d, root := newChunkStorage(t)
	st := d.GetStorage()
	past := time.Now().Add(-driver.StaleStagingAge - time.Hour)
	fresh := filepath.Join(root, driver.StagingName(st))
	stale := filepath.Join(root, driver.StagingNameAt(st, past))
	busy := filepath.Join(root, driver.StagingNameAt(st, past))
	other := filepath.Join(root, driver.StagingNameAt(&model.Storage{ID: st.ID + 1}, past))
	file := filepath.Join(root, driver.StagingNameAt(st, past))
	for _, dir := range []string{fresh, stale, busy, other} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fresh, stale, other, file} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "visible.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Base(other), filepath.Base(file), "visible.txt"}
	sort.Strings(want)
	if names := listNames(t, d); !slices.Equal(names, want) {
		t.Fatalf("listing = %v, want %v", names, want)
	}
	for _, p := range []string{fresh, busy, other, file} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s was reclaimed: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a stale staging folder was not reclaimed: %v", err)
	}
}

// A user's own entries stay listed and untouched whatever their names look
// like: staging folders are recognized by a tag only this server computes for
// this storage, never by name and age alone.
func TestChunkListKeepsUserEntriesNamedLikeStaging(t *testing.T) {
	d, root := newChunkStorage(t)
	folder := filepath.Join(root, ".openlist-upload-0123456789abcdef")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, ".openlist-upload-ABCDEFGHabcdefgh")
	if err := os.WriteFile(file, []byte("mine too"), 0o644); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-25 * time.Hour)
	for _, p := range []string{folder, file} {
		if err := os.Chtimes(p, past, past); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Base(file), filepath.Base(folder)}
	sort.Strings(want)
	if names := listNames(t, d); !slices.Equal(names, want) {
		t.Fatalf("listing = %v, want %v", names, want)
	}
	for _, p := range []string{filepath.Join(folder, "notes.txt"), file} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("a listing removed %s: %v", p, err)
		}
	}
}
