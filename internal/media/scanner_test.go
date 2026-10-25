package media

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestVideoTypeForTypeTag(t *testing.T) {
	cases := map[string]string{
		"":           "",
		"   ":        "",
		"电影":         videoTypeMovie,
		"Movie":      videoTypeMovie,
		"电视剧":        videoTypeTV,
		" 电 视 剧 ":    videoTypeTV,
		"TV":         videoTypeTV,
		"tvshows":    videoTypeTV,
		"动漫":         videoTypeTV,
		"综艺":         videoTypeTV,
		"纪录片":        videoTypeTV,
		"国产剧":        videoTypeTV,
		"电视剧(国产)":    videoTypeTV,
		"4K电影":       videoTypeMovie,
		"随便写的标签":     "",
		"电影 / 电视剧混合": videoTypeTV, // 同时命中时剧集优先
	}
	for tag, want := range cases {
		if got := VideoTypeForTypeTag(tag); got != want {
			t.Errorf("VideoTypeForTypeTag(%q) = %q, want %q", tag, got, want)
		}
	}
}

func TestMediaItemSizeCheck(t *testing.T) {
	long := make([]rune, model.MediaIndexColumnMax+1)
	for i := range long {
		long[i] = '目'
	}
	if err := mediaItemSizeCheck(&model.MediaItem{FolderPath: "/a", FileName: "b"}); err != nil {
		t.Fatalf("short item rejected: %v", err)
	}
	if err := mediaItemSizeCheck(&model.MediaItem{FolderPath: "/a", FileName: string(long)}); err == nil {
		t.Fatal("over-long file name accepted")
	}
	// 按字符数而不是字节数：191 个汉字必须能通过
	ok := string(long[:model.MediaIndexColumnMax])
	if err := mediaItemSizeCheck(&model.MediaItem{FolderPath: "/a", FileName: ok}); err != nil {
		t.Fatalf("191-rune name rejected: %v", err)
	}
}

// fakeTree 用 map 模拟 VFS：key 是目录，value 是目录下的条目名（以 / 结尾表示子目录）。
func fakeTree(tree map[string][]string) listDirFunc {
	return func(_ context.Context, dir string) ([]model.Obj, error) {
		names, ok := tree[dir]
		if !ok {
			return nil, errors.New("not found: " + dir)
		}
		objs := make([]model.Obj, 0, len(names))
		for _, n := range names {
			isDir := strings.HasSuffix(n, "/")
			objs = append(objs, &model.Object{Name: strings.TrimSuffix(n, "/"), IsFolder: isDir})
		}
		return objs, nil
	}
}

func collectMerge(t *testing.T, tree map[string][]string, root string) []string {
	t.Helper()
	var got []string
	err := walkMergeFoldersWith(context.Background(), fakeTree(tree), root, model.MediaTypeVideo, 0, func(p string) error {
		got = append(got, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}

// 需求 1：合并模式要扫描每一层，有视频的文件夹就是一个有效集合。
func TestWalkMergeFoldersScansEveryLevel(t *testing.T) {
	tree := map[string][]string{
		"/A":            {"散落.mkv", "电视剧/", "电影/", "空目录/", "说明.txt"},
		"/A/电视剧":        {"剧A/", "剧B/"},
		"/A/电视剧/剧A":     {"S01/", "S02/"},
		"/A/电视剧/剧A/S01": {"E01.mkv", "E02.mkv"},
		"/A/电视剧/剧A/S02": {"E01.mkv"},
		"/A/电视剧/剧B":     {"1.mp4", "2.mp4"},
		"/A/电影":         {"片.mkv"},
		"/A/空目录":        {"readme.txt"},
	}
	got := collectMerge(t, tree, "/A")
	want := []string{
		"/A/散落.mkv",     // 根下散落的文件仍是独立条目
		"/A/电影",         // 第二层：直接装了视频
		"/A/电视剧/剧A/S01", // 第四层
		"/A/电视剧/剧A/S02",
		"/A/电视剧/剧B", // 第三层
	}
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// 装了视频的文件夹是一个集合，不再向下拆分，避免父子重复入库。
func TestWalkMergeFoldersStopsAtCollection(t *testing.T) {
	tree := map[string][]string{
		"/A":      {"剧/"},
		"/A/剧":    {"E01.mkv", "花絮/"},
		"/A/剧/花絮": {"x.mkv"},
	}
	got := collectMerge(t, tree, "/A")
	if len(got) != 1 || got[0] != "/A/剧" {
		t.Fatalf("got %v, want only /A/剧", got)
	}
}

func TestWalkMergeFoldersSurvivesListError(t *testing.T) {
	tree := map[string][]string{
		"/A":   {"坏/", "好/"},
		"/A/好": {"a.mkv"},
		// "/A/坏" 缺失 → 列目录报错，应跳过而不是中断
	}
	got := collectMerge(t, tree, "/A")
	if len(got) != 1 || got[0] != "/A/好" {
		t.Fatalf("got %v, want [/A/好]", got)
	}
}

func TestWalkMergeFoldersDepthLimit(t *testing.T) {
	tree := map[string][]string{}
	dir := "/A"
	for i := 0; i < maxMergeScanDepth+5; i++ {
		tree[dir] = []string{"d/"}
		dir += "/d"
	}
	tree[dir] = []string{"deep.mkv"}
	if got := collectMerge(t, tree, "/A"); len(got) != 0 {
		t.Fatalf("expected depth limit to stop the walk, got %v", got)
	}
}
