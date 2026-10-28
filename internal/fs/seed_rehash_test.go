package fs

import (
	"context"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

func TestRehashSeedChecksEveryFilePermission(t *testing.T) {
	oldMeta, oldOpen := seedSourceMetaProvider, seedSourceStreamOpener
	t.Cleanup(func() { seedSourceMetaProvider, seedSourceStreamOpener = oldMeta, oldOpen })
	for _, format := range []string{"torrent", "cas"} {
		t.Run(format, func(t *testing.T) {
			var checked, opened []string
			seedSourceMetaProvider = func(path string) (*model.Meta, error) {
				checked = append(checked, path)
				if strings.Contains(path, "/private/") {
					return &model.Meta{Path: "/vault/private", ReadUsers: []uint{2}, ReadUsersSub: true}, nil
				}
				return nil, errs.MetaNotFound
			}
			seedSourceStreamOpener = func(_ context.Context, path string, _ int64) (io.ReadCloser, error) {
				opened = append(opened, path)
				return io.NopCloser(strings.NewReader("abc")), nil
			}
			seed := &torrent.Seed{PieceSize: torrent.DefaultPieceSize, Files: []torrent.SeedFile{
				{Path: "public/a.bin", Size: 3}, {Path: "private/b.bin", Size: 3},
			}}
			_, err := RehashSeedFromFiles(context.Background(), &model.User{ID: 1, BasePath: "/vault"}, seed, "/", format)
			if !errors.Is(err, errs.PermissionDenied) {
				t.Fatalf("error = %v, want permission denied", err)
			}
			if !reflect.DeepEqual(checked, []string{"/vault/public/a.bin", "/vault/private/b.bin"}) {
				t.Fatalf("checked = %v", checked)
			}
			if !reflect.DeepEqual(opened, []string{"/vault/public/a.bin"}) {
				t.Fatalf("opened restricted source: %v", opened)
			}
		})
	}
}

func TestOpenSeedSourceMetadataErrorsAndWhitelist(t *testing.T) {
	oldMeta, oldOpen := seedSourceMetaProvider, seedSourceStreamOpener
	t.Cleanup(func() { seedSourceMetaProvider, seedSourceStreamOpener = oldMeta, oldOpen })
	user := &model.User{ID: 1, BasePath: "/"}
	lookupErr := errors.New("metadata unavailable")
	for _, tc := range []struct {
		name string
		meta *model.Meta
		err  error
		want error
	}{
		{"lookup failure", nil, lookupErr, lookupErr},
		{"no metadata", nil, errors.Wrap(errs.MetaNotFound, "lookup"), nil},
		{"allowed user", &model.Meta{Path: "/private", ReadUsers: []uint{1}, ReadUsersSub: true}, nil, nil},
		{"denied user", &model.Meta{Path: "/private", ReadUsers: []uint{2}, ReadUsersSub: true}, nil, errs.PermissionDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opened := false
			seedSourceMetaProvider = func(string) (*model.Meta, error) { return tc.meta, tc.err }
			seedSourceStreamOpener = func(context.Context, string, int64) (io.ReadCloser, error) {
				opened = true
				return io.NopCloser(strings.NewReader("abc")), nil
			}
			rc, err := openSeedSource(context.Background(), user, "/private/a.bin", 3)
			if rc != nil {
				_ = rc.Close()
			}
			if !errors.Is(err, tc.want) || opened != (tc.want == nil) {
				t.Fatalf("error = %v, opened = %v", err, opened)
			}
		})
	}
}

type seedTestReadCloser struct {
	io.Reader
	closed int
	read   int
}

func (r *seedTestReadCloser) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

func (r *seedTestReadCloser) Close() error {
	r.closed++
	return nil
}

func TestOpenSeedLinkStreamLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name      string
		size      int64
		actual    int64
		invalid   bool
		rangeFail bool
	}{
		{name: "success", size: 3, actual: 3},
		{name: "actual size mismatch", size: 3, actual: 4},
		{name: "actual size over budget", size: 3, actual: MaxSeedGenerateSyncSize + 1},
		{name: "negative size", size: -1, actual: -1},
		{name: "size overflow", size: math.MaxInt64, actual: math.MaxInt64},
		{name: "invalid link", size: 3, actual: 3, invalid: true},
		{name: "range failure", size: 3, actual: 3, rangeFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			linkCloser := &seedTestReadCloser{}
			body := &seedTestReadCloser{Reader: strings.NewReader("abcdef")}
			calls := 0
			link := &model.Link{SyncClosers: utils.NewSyncClosers(linkCloser)}
			if !tc.invalid {
				link.RangeReader = stream.RangeReaderFunc(func(_ context.Context, r http_range.Range) (io.ReadCloser, error) {
					calls++
					if r.Length != tc.actual {
						t.Fatalf("range length = %d, want %d", r.Length, tc.actual)
					}
					if tc.rangeFail {
						return body, errors.New("range failure")
					}
					return body, nil
				})
			}
			rc, err := openSeedLinkStream(context.Background(), link, &model.Object{Size: tc.actual}, tc.size)
			wantSuccess := tc.name == "success"
			if (err == nil) != wantSuccess {
				t.Fatalf("error = %v", err)
			}
			if rc != nil {
				data, readErr := io.ReadAll(rc)
				if readErr != nil || string(data) != "abcd" || body.read != 4 {
					t.Fatalf("read = %q, consumed = %d, error = %v", data, body.read, readErr)
				}
				_ = rc.Close()
				_ = rc.Close()
			}
			if linkCloser.closed != 1 {
				t.Fatalf("link closes = %d", linkCloser.closed)
			}
			if calls != body.closed {
				t.Fatalf("range calls = %d, body closes = %d", calls, body.closed)
			}
		})
	}
}

func TestRehashSeedReadBound(t *testing.T) {
	oldMeta, oldOpen := seedSourceMetaProvider, seedSourceStreamOpener
	t.Cleanup(func() { seedSourceMetaProvider, seedSourceStreamOpener = oldMeta, oldOpen })
	seedSourceMetaProvider = func(string) (*model.Meta, error) { return nil, errs.MetaNotFound }
	for _, format := range []string{"torrent", "cas"} {
		for _, data := range []string{"ab", "abcdef"} {
			t.Run(format+data, func(t *testing.T) {
				body := &seedTestReadCloser{Reader: strings.NewReader(data)}
				seedSourceStreamOpener = func(context.Context, string, int64) (io.ReadCloser, error) { return body, nil }
				seed := &torrent.Seed{PieceSize: torrent.DefaultPieceSize, Files: []torrent.SeedFile{{Path: "a.bin", Size: 3}}}
				_, err := RehashSeedFromFiles(context.Background(), &model.User{BasePath: "/"}, seed, "/", format)
				if err == nil || body.read > 4 || body.closed != 1 {
					t.Fatalf("error = %v, read = %d, closes = %d", err, body.read, body.closed)
				}
			})
		}
	}
}

func TestRehashSeedHashBudget(t *testing.T) {
	if total, err := addSeedHashSize(0, MaxSeedGenerateSyncSize); err != nil || total != MaxSeedGenerateSyncSize {
		t.Fatalf("exact budget: total = %d, error = %v", total, err)
	}
	for _, sizes := range [][]int64{{-1}, {MaxSeedGenerateSyncSize + 1}, {MaxSeedGenerateSyncSize, 1}, {math.MaxInt64}} {
		files := make([]torrent.SeedFile, len(sizes))
		for i, size := range sizes {
			files[i] = torrent.SeedFile{Path: "a.bin", Size: size}
		}
		for _, format := range []string{"torrent", "cas"} {
			seed := &torrent.Seed{Files: files}
			_, err := RehashSeedFromFiles(context.Background(), &model.User{BasePath: "/"}, seed, "/", format)
			if err == nil || !strings.Contains(err.Error(), "1GB") {
				t.Fatalf("sizes = %v, format = %s, error = %v", sizes, format, err)
			}
		}
	}
}

func TestAcquireSeedHashSlotCancellation(t *testing.T) {
	first, err := AcquireSeedHashSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer first()
	second, err := AcquireSeedHashSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer second()
	if len(seedHashSlots) != cap(seedHashSlots) {
		t.Fatalf("slots = %d", len(seedHashSlots))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		release, acquireErr := AcquireSeedHashSlot(ctx)
		if release != nil {
			release()
		}
		done <- acquireErr
	}()
	cancel()
	select {
	case acquireErr := <-done:
		if !errors.Is(acquireErr, context.Canceled) {
			t.Fatalf("error = %v", acquireErr)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled acquisition remained blocked")
	}
	first()
	first()
	third, err := AcquireSeedHashSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	third()
}

func TestRehashCASPieceSizeInvalidatesOnlyPieceHashes(t *testing.T) {
	oldMeta, oldOpen := seedSourceMetaProvider, seedSourceStreamOpener
	t.Cleanup(func() { seedSourceMetaProvider, seedSourceStreamOpener = oldMeta, oldOpen })
	seedSourceMetaProvider = func(string) (*model.Meta, error) { return nil, errs.MetaNotFound }
	seedSourceStreamOpener = func(context.Context, string, int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("abc")), nil
	}
	for _, pieceSize := range []int64{1, 0, torrent.DefaultPieceSize} {
		t.Run(fmt.Sprintf("piece-%d", pieceSize), func(t *testing.T) {
			seed := &torrent.Seed{
				PieceSize: pieceSize, GlobalPieces: []string{"old-global"}, AvailablePieceHashes: []string{"sha1", "sha256"},
				Files: []torrent.SeedFile{{Path: "a.bin", Size: 3, Hashes: torrent.SeedHashes{
					SHA1: "whole-sha1", SHA256: "whole-sha256", GCID: "whole-gcid",
					Pieces: &torrent.SeedPieceHashes{MD5: []string{"old-md5"}, SHA1: []string{"old-sha1"}, SHA256: []string{"old-sha256"}},
				}}},
			}
			_, err := RehashSeedFromFiles(context.Background(), &model.User{BasePath: "/"}, seed, "/", "cas")
			if err != nil {
				t.Fatal(err)
			}
			hashes := seed.Files[0].Hashes
			if seed.PieceSize != torrent.DefaultPieceSize || hashes.SHA1 != "whole-sha1" || hashes.SHA256 != "whole-sha256" || hashes.GCID != "whole-gcid" {
				t.Fatalf("changed whole hashes: %+v", hashes)
			}
			if hashes.MD5 != "900150983cd24fb0d6963f7d28e17f72" || !reflect.DeepEqual(hashes.Pieces.MD5, []string{hashes.MD5}) {
				t.Fatalf("wrong MD5 matrix: %+v", hashes)
			}
			if pieceSize == torrent.DefaultPieceSize {
				if !reflect.DeepEqual(hashes.Pieces.SHA1, []string{"old-sha1"}) || !reflect.DeepEqual(hashes.Pieces.SHA256, []string{"old-sha256"}) || len(seed.GlobalPieces) != 1 {
					t.Fatal("same-sized piece hashes were discarded")
				}
			} else if len(hashes.Pieces.SHA1) != 0 || len(hashes.Pieces.SHA256) != 0 || len(seed.GlobalPieces) != 0 || len(seed.AvailablePieceHashes) != 0 {
				t.Fatalf("stale piece hashes survived: %+v", seed)
			}
		})
	}
}

func TestRehashSeedFromFilesWithMatrix(t *testing.T) {
	oldMeta, oldOpen := seedSourceMetaProvider, seedSourceStreamOpener
	t.Cleanup(func() { seedSourceMetaProvider, seedSourceStreamOpener = oldMeta, oldOpen })
	seedSourceMetaProvider = func(string) (*model.Meta, error) { return nil, errs.MetaNotFound }
	contents := []string{strings.Repeat("a", 20000), strings.Repeat("b", 20000)}
	seedSourceStreamOpener = func(_ context.Context, path string, _ int64) (io.ReadCloser, error) {
		index := 0
		if strings.HasSuffix(path, "b.bin") {
			index = 1
		}
		return io.NopCloser(strings.NewReader(contents[index])), nil
	}
	for _, tc := range []struct {
		name       string
		format     string
		request    SeedHashMatrix
		want       SeedHashMatrix
		legacy     bool
		wantGlobal bool
		oldGlobal  bool
	}{
		{name: "legacy torrent required only", format: "torrent", legacy: true, want: SeedHashMatrix{SHA1: SeedHashSelection{Whole: true, Pieces: true}}, wantGlobal: true, oldGlobal: true},
		{name: "legacy CAS required only", format: "cas", legacy: true, want: SeedHashMatrix{MD5: SeedHashSelection{Whole: true, Pieces: true}}, oldGlobal: true},
		{name: "legacy OSS no implicit algorithm", format: "oss", legacy: true},
		{name: "OSS explicit matrix", format: "oss", request: SeedHashMatrix{MD5: SeedHashSelection{Whole: true}, SHA256: SeedHashSelection{Pieces: true}}, want: SeedHashMatrix{MD5: SeedHashSelection{Whole: true}, SHA256: SeedHashSelection{Pieces: true}}},
		{name: "torrent explicit extra algorithms", format: "torrent", request: SeedHashMatrix{MD5: SeedHashSelection{Pieces: true}, SHA256: SeedHashSelection{Whole: true}}, want: SeedHashMatrix{MD5: SeedHashSelection{Pieces: true}, SHA1: SeedHashSelection{Whole: true, Pieces: true}, SHA256: SeedHashSelection{Whole: true}}, wantGlobal: true},
		{name: "CAS explicit extra algorithms", format: "cas", request: SeedHashMatrix{SHA1: SeedHashSelection{Whole: true}, SHA256: SeedHashSelection{Pieces: true}}, want: SeedHashMatrix{MD5: SeedHashSelection{Whole: true, Pieces: true}, SHA1: SeedHashSelection{Whole: true}, SHA256: SeedHashSelection{Pieces: true}}},
		{name: "OSS refresh existing global", format: "oss", request: SeedHashMatrix{SHA1: SeedHashSelection{Pieces: true}}, want: SeedHashMatrix{SHA1: SeedHashSelection{Pieces: true}}, wantGlobal: true, oldGlobal: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := torrent.NewSeed("matrix", "OpenList", 16*1024)
			seed.Files = []torrent.SeedFile{{Path: "a.bin", Size: int64(len(contents[0]))}, {Path: "b.bin", Size: int64(len(contents[1]))}}
			if tc.oldGlobal {
				seed.GlobalPieces = []string{strings.Repeat("0", 40), strings.Repeat("0", 40), strings.Repeat("0", 40)}
			}
			var pieces []byte
			var err error
			if tc.legacy {
				pieces, err = RehashSeedFromFiles(context.Background(), &model.User{BasePath: "/"}, seed, "/", tc.format)
			} else {
				pieces, err = RehashSeedFromFilesWithMatrix(context.Background(), &model.User{BasePath: "/"}, seed, "/", tc.format, tc.request)
			}
			if err != nil {
				t.Fatal(err)
			}
			for i := range seed.Files {
				hasher := torrent.NewHashWriter(seed.PieceSize, seed.PieceSize, 0)
				_, _ = io.WriteString(hasher, contents[i])
				hasher.Finish()
				expected := hasher.BuildSeedFile(seed.Files[i].Path, "")
				applySeedMatrix(&expected, tc.want)
				expected.Hashes.GCID = ""
				if !reflect.DeepEqual(seed.Files[i].Hashes, expected.Hashes) {
					t.Fatalf("file %d hashes = %+v, want %+v", i, seed.Files[i].Hashes, expected.Hashes)
				}
			}
			if tc.wantGlobal {
				global := torrent.NewHashWriter(seed.PieceSize, seed.PieceSize, 0)
				for _, content := range contents {
					_, _ = io.WriteString(global, content)
				}
				global.Finish()
				if !reflect.DeepEqual(seed.GlobalPieces, global.GetPieceSHA1s()) || !reflect.DeepEqual(pieces, global.GetPieceHashes()) {
					t.Fatal("global hashes are stale or use per-file boundaries")
				}
			} else if len(pieces) != 0 || len(seed.GlobalPieces) != 0 {
				t.Fatal("unrequested global hashes were added or stale hashes survived")
			}
			if err := torrent.ValidateSeed(seed, torrent.DefaultParseLimits()); err != nil {
				t.Fatalf("rehash output validation: %v", err)
			}
		})
	}
}

func TestRehashMatrixPreservesUnrequestedExistingHashes(t *testing.T) {
	oldMeta, oldOpen := seedSourceMetaProvider, seedSourceStreamOpener
	t.Cleanup(func() { seedSourceMetaProvider, seedSourceStreamOpener = oldMeta, oldOpen })
	seedSourceMetaProvider = func(string) (*model.Meta, error) { return nil, errs.MetaNotFound }
	seedSourceStreamOpener = func(context.Context, string, int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("abc")), nil
	}
	seed := &torrent.Seed{PieceSize: torrent.DefaultPieceSize, Files: []torrent.SeedFile{{Path: "a.bin", Size: 3, Hashes: torrent.SeedHashes{
		SHA1: "existing-sha1", GCID: "existing-gcid", Pieces: &torrent.SeedPieceHashes{SHA256: []string{"existing-sha256-piece"}},
	}}}}
	_, err := RehashSeedFromFilesWithMatrix(context.Background(), &model.User{BasePath: "/"}, seed, "/", "oss", SeedHashMatrix{MD5: SeedHashSelection{Whole: true}})
	if err != nil {
		t.Fatal(err)
	}
	hashes := seed.Files[0].Hashes
	if hashes.MD5 != "900150983cd24fb0d6963f7d28e17f72" || hashes.SHA1 != "existing-sha1" || hashes.GCID != "existing-gcid" || !reflect.DeepEqual(hashes.Pieces.SHA256, []string{"existing-sha256-piece"}) || len(hashes.Pieces.MD5) != 0 || len(hashes.Pieces.SHA1) != 0 {
		t.Fatalf("unrequested existing hashes changed: %+v", hashes)
	}
}
