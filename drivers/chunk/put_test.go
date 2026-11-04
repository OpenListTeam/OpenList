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
	"runtime"
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
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
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
	return newChunkStorageWith(t, "")
}

// newChunkStorageWith is newChunkStorage with extra chunk settings, given as
// JSON object members such as `"chunk_large_file_only":true`.
func newChunkStorageWith(t *testing.T, extra string) (driver.Driver, string) {
	t.Helper()
	if extra != "" {
		extra = "," + extra
	}
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
		Addition:  fmt.Sprintf(`{"remote_path":%q,"part_size":%d,"show_hidden":true,"num_list_workers":2%s}`, localMount, testPartSize, extra),
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

// backupLeftovers lists the previous versions an overwrite left on the remote.
func backupLeftovers(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.Contains(e.Name(), backupInfix) {
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
	if left := backupLeftovers(t, root); len(left) != 0 {
		t.Fatalf("previous version left behind: %v", left)
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

// When the new version cannot be moved into place and the previous one cannot
// be put back either, the previous version is kept where the user can see it,
// the error says where, and no listing reclaims it however old it is.
func TestChunkKeepsUnrestorableBackup(t *testing.T) {
	d, root := newChunkStorage(t)
	ctx := context.Background()
	old := content(3*testPartSize, 'a')
	if err := putFile(ctx, d, "doc.bin", bytes.NewReader(old), int64(len(old))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	orig := renameOnRemote
	t.Cleanup(func() { renameOnRemote = orig })
	boom := errors.New("remote went away")
	renameOnRemote = func(ctx context.Context, storage driver.Driver, srcPath, dstName string) error {
		// moving the new version in, and putting the previous one back
		if dstName == "[openlist_chunk]doc.bin" {
			return boom
		}
		return orig(ctx, storage, srcPath, dstName)
	}
	err := putFile(ctx, d, "doc.bin", bytes.NewReader(content(2*testPartSize, 'b')), 2*testPartSize)
	if !errors.Is(err, boom) {
		t.Fatalf("overwrite returned %v, want %v", err, boom)
	}
	renameOnRemote = orig

	entries, err2 := os.ReadDir(root)
	if err2 != nil {
		t.Fatal(err2)
	}
	var backup string
	var all []string
	for _, e := range entries {
		all = append(all, e.Name())
		if name, ok := strings.CutPrefix(e.Name(), "[openlist_chunk]doc.bin"); ok && name != "" {
			backup = e.Name()
		}
	}
	if backup == "" {
		t.Fatalf("the previous version is gone; remote holds %v", all)
	}
	shown := strings.TrimPrefix(backup, "[openlist_chunk]")
	if !strings.Contains(err.Error(), shown) {
		t.Fatalf("error %q does not say the previous version was kept as %s", err, shown)
	}
	past := time.Now().Add(-25 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, backup), past, past); err != nil {
		t.Fatal(err)
	}
	if names := listNames(t, d); !slices.Contains(names, shown) {
		t.Fatalf("listing = %v, want it to show the kept previous version %s", names, shown)
	}
	if got := readChunked(t, root, shown); !bytes.Equal(got, old) {
		t.Fatalf("kept previous version is damaged or gone (len %d)", len(got))
	}
	if left := stagingLeftovers(t, root); len(left) != 0 {
		t.Fatalf("staging left behind: %v", left)
	}
}

// With ChunkLargeFileOnly, an overwrite across the threshold replaces the
// previous version whichever way it was stored: no second entry is left, and
// Get serves the new content. A user's own entry that only shares the name of
// the other form stays.
func TestChunkOverwriteAcrossTheThreshold(t *testing.T) {
	d, root := newChunkStorageWith(t, `"chunk_large_file_only":true`)
	ctx := context.Background()
	small, large := content(500, 'a'), content(3*testPartSize, 'b')
	check := func(want []byte) {
		t.Helper()
		if names := listNames(t, d); len(names) != 1 || names[0] != "f.bin" {
			t.Fatalf("listing = %v, want [f.bin]", names)
		}
		obj, err := op.Get(ctx, d, "/f.bin")
		if err != nil {
			t.Fatal(err)
		}
		if obj.GetSize() != int64(len(want)) {
			t.Fatalf("Get reports %d bytes, want %d", obj.GetSize(), len(want))
		}
	}
	for _, body := range [][]byte{small, large, small} {
		if err := putFile(ctx, d, "f.bin", bytes.NewReader(body), int64(len(body))); err != nil {
			t.Fatalf("upload of %d bytes: %v", len(body), err)
		}
		check(body)
	}
	if got, err := os.ReadFile(filepath.Join(root, "f.bin")); err != nil || !bytes.Equal(got, small) {
		t.Fatalf("whole file = %d bytes, %v", len(got), err)
	}
	// a plain file named like a chunk folder is not one
	lookalike := filepath.Join(root, "[openlist_chunk]g.bin")
	if err := os.WriteFile(lookalike, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := putFile(ctx, d, "g.bin", bytes.NewReader(small), int64(len(small))); err != nil {
		t.Fatalf("upload: %v", err)
	}
	if _, err := os.Stat(lookalike); err != nil {
		t.Fatalf("a user's file named like a chunk folder was removed: %v", err)
	}
}

// Entries of a chunk folder that are not part indexes, a negative number among
// them, are no parts: listing or getting the file must not crash on them.
func TestChunkIgnoresEntriesThatAreNoPartIndex(t *testing.T) {
	d, root := newChunkStorage(t)
	dir := filepath.Join(root, "[openlist_chunk]x.bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"0": "ab", "-1": "zz", "notes": "n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if names := listNames(t, d); len(names) != 1 || names[0] != "x.bin" {
		t.Fatalf("listing = %v, want [x.bin]", names)
	}
	obj, err := op.Get(context.Background(), d, "/x.bin")
	if err != nil || obj.GetSize() != 2 {
		t.Fatalf("Get = %v, %v; want the 2-byte file", obj, err)
	}
}

// A part number is the remote's word. One beyond the number of entries leaves
// a gap, so the folder holds no file, and nothing is allocated up to it: the
// largest number crashed the server, and a large one took gigabytes.
func TestChunkAllocatesNothingForHugePartNumbers(t *testing.T) {
	for _, idx := range []string{"9223372036854775807", "1000000000"} {
		d, root := newChunkStorage(t)
		dir := filepath.Join(root, "[openlist_chunk]huge")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string][]byte{"0": content(testPartSize, 'a'), idx: []byte("x")} {
			if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		names := listNames(t, d)
		_, getErr := op.Get(context.Background(), d, "/huge")
		runtime.ReadMemStats(&after)
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 64<<20 {
			t.Errorf("part %s: reading the folder allocated %d bytes", idx, grown)
		}
		if len(names) != 1 || names[0] != "[openlist_chunk]huge" {
			t.Errorf("part %s: listing = %v, want the folder under its own name", idx, names)
		}
		if !errs.IsObjectNotFound(getErr) {
			t.Errorf("part %s: Get = %v, want not found", idx, getErr)
		}
	}
}

// On a remote that takes names differing only in case for one, as the default
// macOS and Windows file systems do, an overwrite under another case replaces
// the file instead of failing to move the new version in.
func TestChunkOverwriteUnderAnotherCase(t *testing.T) {
	d, root := newChunkStorage(t)
	probe := filepath.Join(root, "CaseProbe")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := os.Stat(filepath.Join(root, "caseprobe"))
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Skip("needs a file system that ignores case")
	}
	ctx := context.Background()
	if err := putFile(ctx, d, "report.bin", bytes.NewReader(content(2*testPartSize, 'a')), 2*testPartSize); err != nil {
		t.Fatalf("seed: %v", err)
	}
	body := content(3*testPartSize, 'b')
	if err := putFile(ctx, d, "REPORT.BIN", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("overwrite under another case: %v", err)
	}
	if got := readChunked(t, root, "REPORT.BIN"); !bytes.Equal(got, body) {
		t.Fatalf("the file holds %d bytes, want the %d just written", len(got), len(body))
	}
	if left := append(backupLeftovers(t, root), stagingLeftovers(t, root)...); len(left) != 0 {
		t.Fatalf("left behind: %v", left)
	}
}
