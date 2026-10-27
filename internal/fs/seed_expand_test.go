package fs

import "testing"

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
	got := finalizeSeedEntries(raw, []string{"/剧"}, true)
	if len(got) != 2 {
		t.Fatalf("entries = %d", len(got))
	}
	if got[0].SeedPath != "剧/S01/01.mkv" || got[1].SeedPath != "剧/S02/01.mkv" {
		t.Fatalf("保留结构时路径不对: %q, %q", got[0].SeedPath, got[1].SeedPath)
	}
}

func TestFinalizeSeedEntriesFlattenDedupes(t *testing.T) {
	raw := []rawSeedEntry{
		{requested: "/剧/S01/01.mkv", full: "/剧/S01/01.mkv"},
		{requested: "/剧/S02/01.mkv", full: "/剧/S02/01.mkv"},
		{requested: "/剧/S02/02.mkv", full: "/剧/S02/02.mkv"},
	}
	got := finalizeSeedEntries(raw, []string{"/剧"}, false)
	want := []string{"01.mkv", "01 (2).mkv", "02.mkv"}
	for i := range want {
		if got[i].SeedPath != want[i] {
			t.Fatalf("拍平后第 %d 个路径 = %q, want %q", i, got[i].SeedPath, want[i])
		}
	}
}

func TestFinalizeSeedEntriesSingleFile(t *testing.T) {
	raw := []rawSeedEntry{{requested: "/电影/a.mkv", full: "/电影/a.mkv"}}
	got := finalizeSeedEntries(raw, []string{"/电影/a.mkv"}, false)
	if len(got) != 1 || got[0].SeedPath != "a.mkv" {
		t.Fatalf("单文件种子路径 = %+v", got)
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
