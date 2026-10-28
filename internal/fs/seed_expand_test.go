package fs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/glebarez/sqlite"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

func TestDeriveSeedName(t *testing.T) {
	cases := []struct {
		paths []string
		want  string
	}{
		{[]string{"/电影/流浪地球.mkv"}, "流浪地球"},
		{[]string{"/电视剧/群星"}, "群星"},
		{[]string{"/a/01.mp3", "/a/02.mp3"}, "torrent"},
		{[]string{"/a/x.mkv", "/b/y.mkv"}, "torrent"},
		{nil, "torrent"},
	}
	for _, c := range cases {
		if got := DeriveSeedName(c.paths); got != c.want {
			t.Errorf("DeriveSeedName(%v) = %q, want %q", c.paths, got, c.want)
		}
	}
}

func TestFinalizeSeedEntriesPreserve(t *testing.T) {
	raw := []rawSeedEntry{
		{requested: "/剧/S01/01.mkv", full: "/剧/S01/01.mkv"},
		{requested: "/剧/S02/01.mkv", full: "/剧/S02/01.mkv"},
	}
	// 只选了一个目录：种子根就是该目录，路径不能再重复一层目录名
	got := finalizeSeedEntries(raw, true, "/剧")
	if len(got) != 2 {
		t.Fatalf("entries = %d", len(got))
	}
	if got[0].SeedPath != "S01/01.mkv" || got[1].SeedPath != "S02/01.mkv" {
		t.Fatalf("保留结构时路径不对: %q, %q", got[0].SeedPath, got[1].SeedPath)
	}
}

func TestFinalizeSeedEntriesPreserveMultiRoot(t *testing.T) {
	raw := []rawSeedEntry{
		{requested: "/剧/A/01.mkv", full: "/剧/A/01.mkv"},
		{requested: "/影/B/02.mkv", full: "/影/B/02.mkv"},
	}
	// 多选时根是公共父目录，各自目录名要保留
	got := finalizeSeedEntries(raw, true, "/")
	if got[0].SeedPath != "剧/A/01.mkv" || got[1].SeedPath != "影/B/02.mkv" {
		t.Fatalf("多选保留结构时路径不对: %q, %q", got[0].SeedPath, got[1].SeedPath)
	}
}

func TestFinalizeSeedEntriesFlattenDedupes(t *testing.T) {
	raw := []rawSeedEntry{
		{requested: "/剧/S01/01.mkv", full: "/剧/S01/01.mkv"},
		{requested: "/剧/S02/01.mkv", full: "/剧/S02/01.mkv"},
		{requested: "/剧/S02/02.mkv", full: "/剧/S02/02.mkv"},
	}
	got := finalizeSeedEntries(raw, false, "/剧")
	want := []string{"01.mkv", "01 (2).mkv", "02.mkv"}
	for i := range want {
		if got[i].SeedPath != want[i] {
			t.Fatalf("拍平后第 %d 个路径 = %q, want %q", i, got[i].SeedPath, want[i])
		}
	}
}

func TestFinalizeSeedEntriesSingleFile(t *testing.T) {
	raw := []rawSeedEntry{{requested: "/电影/a.mkv", full: "/电影/a.mkv"}}
	got := finalizeSeedEntries(raw, false, "/电影")
	if len(got) != 1 || got[0].SeedPath != "a.mkv" {
		t.Fatalf("单文件种子路径 = %+v", got)
	}
}

func TestFinalizeSeedEntriesWithUserBasePath(t *testing.T) {
	for _, tc := range []struct {
		root      string
		requested string
		want      string
	}{
		{"/剧", "/剧/S01/01.mkv", "S01/01.mkv"},
		{"/", "/剧/S01/01.mkv", "剧/S01/01.mkv"},
	} {
		raw := []rawSeedEntry{{requested: tc.requested, full: "/vault" + tc.requested}}
		got := finalizeSeedEntries(raw, true, tc.root)
		if got[0].SeedPath != tc.want || got[0].FullPath != raw[0].full {
			t.Fatalf("finalize(%q): %+v, want %q", tc.root, got, tc.want)
		}
	}
}

func TestUniqueSeedArtifactNames(t *testing.T) {
	used := make(map[string]int)
	for i, name := range []string{"a.oss", "a (2).oss", "a.oss", "a.oss"} {
		want := []string{"a.oss", "a (2).oss", "a (3).oss", "a (4).oss"}[i]
		if got := uniqueSeedFileName(name, used); got != want {
			t.Fatalf("name %d = %q, want %q", i, got, want)
		}
	}
}

func TestDedupeSeedPathsExt(t *testing.T) {
	entries := []SeedSourceEntry{
		{SeedPath: "a.tar.gz"},
		{SeedPath: "a.tar.gz"},
		{SeedPath: "a.tar.gz"},
	}
	dedupeSeedPaths(entries)
	want := []string{"a.tar.gz", "a.tar (2).gz", "a.tar (3).gz"}
	for i := range want {
		if entries[i].SeedPath != want[i] {
			t.Fatalf("第 %d 个 = %q, want %q", i, entries[i].SeedPath, want[i])
		}
	}
}

func TestExpandSeedSourcesVFSVisibilityAndSplit(t *testing.T) {
	if db.GetDb() == nil {
		memoryDB, err := gorm.Open(sqlite.Open("file:seed_expand_vfs?mode=memory&cache=shared"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if conf.Conf == nil {
			conf.Conf = conf.DefaultConfig("data")
		}
		db.Init(memoryDB)
	}
	ctx := context.Background()
	const base = "/seed-expansion-vfs"
	mount := func(path string, files []string) {
		root := t.TempDir()
		for _, name := range files {
			full := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("abc"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		addition, err := json.Marshal(map[string]any{"root_folder_path": root, "show_hidden": true})
		if err != nil {
			t.Fatal(err)
		}
		id, err := op.CreateStorage(ctx, model.Storage{Driver: "Local", MountPath: path, Addition: string(addition)})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := op.DeleteStorageById(context.Background(), id); err != nil {
				t.Errorf("delete test storage: %v", err)
			}
		})
	}
	mount(base+"/main", []string{"a.txt", "secret.txt", "hidden-dir/inside.txt", "nested/shadow.txt"})
	mount(base+"/main/nested", []string{"n.txt"})
	mount(base+"/other", []string{"b.txt"})
	for _, meta := range []*model.Meta{
		{Path: base + "/main", Hide: "^secret\\.txt$\n^hidden-dir$", HSub: true},
		{Path: base + "/other", ReadUsers: []uint{2}, ReadUsersSub: true},
	} {
		if err := op.CreateMeta(meta); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := op.DeleteMetaById(meta.ID); err != nil {
				t.Errorf("delete test meta: %v", err)
			}
		})
	}
	user := &model.User{ID: 1, BasePath: base}
	visibleUser := &model.User{ID: 1, BasePath: base, Permission: 1}
	staleCtx := context.WithValue(ctx, conf.UserKey, visibleUser)
	staleCtx = context.WithValue(staleCtx, conf.MetaKey, &model.Meta{Path: base, Hide: ".*", HSub: true})
	for _, tc := range []struct {
		name  string
		user  *model.User
		paths []string
		want  []string
	}{
		{"virtual root", user, []string{"/"}, []string{"main/a.txt", "main/nested/n.txt"}},
		{"single directory", user, []string{"/main"}, []string{"a.txt", "nested/n.txt"}},
		{"root base path", &model.User{ID: 1, BasePath: "/"}, []string{base}, []string{"main/a.txt", "main/nested/n.txt"}},
		{"hidden permission", visibleUser, []string{"/main"}, []string{"a.txt", "hidden-dir/inside.txt", "nested/n.txt", "secret.txt"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, err := ExpandSeedSources(staleCtx, tc.user, tc.paths, true)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(entries))
			for i, entry := range entries {
				got[i] = entry.SeedPath
				full, joinErr := tc.user.JoinPath(entry.Requested)
				if joinErr != nil || entry.FullPath != full {
					t.Fatalf("invalid VFS source: %+v", entry)
				}
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("paths = %v, want %v", got, tc.want)
			}
		})
	}
	for _, hiddenPath := range []string{"/main/secret.txt", "/main/hidden-dir/inside.txt"} {
		if _, err := ExpandSeedSources(ctx, user, []string{hiddenPath}, true); !errors.Is(err, errs.PermissionDenied) {
			t.Fatalf("hidden path %q: error = %v", hiddenPath, err)
		}
	}
	mount(base+"/collisions", []string{"first/a.txt", "second/a (2).txt", "third/a.txt"})
	artifacts, _, err := GenerateSeedArtifacts(ctx, user, SeedGenerateParams{
		Paths: []string{"/collisions"}, Formats: []string{"oss"}, Split: true, PreserveStructure: true,
		HashMatrix: SeedHashMatrix{MD5: SeedHashSelection{Whole: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(artifacts))
	for i, artifact := range artifacts {
		names[i] = artifact.FileName
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{"a (2).oss", "a (3).oss", "a.oss"}) {
		t.Fatalf("split artifacts = %v", names)
	}
}
