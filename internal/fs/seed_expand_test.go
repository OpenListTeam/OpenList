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
