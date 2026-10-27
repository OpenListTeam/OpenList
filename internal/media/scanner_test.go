package media

import (
	"context"
	"encoding/json"
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
		"TV 剧":       videoTypeTV, // 英文关键字后接中文仍应命中
		"itvshows":   "",          // 英文关键字需要词边界，不能被子串误判
		"MTV":        "",
		"IPTV":       "",
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

// 集合目录里的附属目录（花絮 / 样片 / 字幕）不单独成条
func TestWalkMergeFoldersSkipsExtras(t *testing.T) {
	tree := map[string][]string{
		"/A":          {"剧/"},
		"/A/剧":        {"E01.mkv", "花絮/", "Extras/"},
		"/A/剧/花絮":     {"x.mkv"},
		"/A/剧/Extras": {"trailer.mkv"},
	}
	got := collectMerge(t, tree, "/A")
	if len(got) != 1 || got[0] != "/A/剧" {
		t.Fatalf("got %v, want only /A/剧", got)
	}
}

// 集合目录里嵌套的影片目录必须继续扫描：
// 「群星.1080p.内嵌中英双字/群星2」以前会整棵漏掉。
func TestWalkMergeFoldersScansNestedCollections(t *testing.T) {
	tree := map[string][]string{
		"/影视资源": {"群星.1080p.内嵌中英双字/"},
		"/影视资源/群星.1080p.内嵌中英双字":      {"群星.1080p.mkv", "群星2/", "Subs/"},
		"/影视资源/群星.1080p.内嵌中英双字/群星2":  {"群星2.1080p.mkv"},
		"/影视资源/群星.1080p.内嵌中英双字/Subs": {"chs.srt"},
	}
	got := collectMerge(t, tree, "/影视资源")
	want := []string{
		"/影视资源/群星.1080p.内嵌中英双字",
		"/影视资源/群星.1080p.内嵌中英双字/群星2",
	}
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %v\nwant %v", got, want)
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

func TestVideoExtsIncludeISO(t *testing.T) {
	if !isMediaFile("Some.Movie.2010.iso", model.MediaTypeVideo) {
		t.Fatal(".iso 应被识别为视频文件")
	}
	if got := mediaMimeType(".iso"); got != "application/x-iso9660-image" {
		t.Fatalf("mediaMimeType(.iso) = %q", got)
	}
	// 未知扩展名不应 panic（各平台 mime 表覆盖范围不同，结果允许为空）
	_ = mediaMimeType(".no-such-ext")
}

// collectDepth 按「用户配置的合并深度」收集条目（扫描根的直接子文件夹是第 1 层）
func collectDepth(t *testing.T, tree map[string][]string, root string, depth int) []string {
	t.Helper()
	var got []string
	err := walkMergeDepthWith(context.Background(), fakeTree(tree), root, model.MediaTypeVideo, depth, 0, func(p string) error {
		got = append(got, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	return got
}

// 合并深度=1：扫描根的直接子文件夹（剧名）作为条目，
// /TV/群星/第一季、/TV/群星/第二季 归入同一部剧。
func TestWalkMergeDepthTargetsShowLevel(t *testing.T) {
	tree := map[string][]string{
		"/TV":        {"群星/", "散落.mkv", "说明.txt"},
		"/TV/群星":     {"第一季/", "第二季/"},
		"/TV/群星/第一季": {"01.mkv", "02.mkv"},
		"/TV/群星/第二季": {"01.mkv"},
	}
	got := collectDepth(t, tree, "/TV", 1)
	want := []string{"/TV/群星", "/TV/散落.mkv"}
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// 合并深度=2：条目下钻到季目录（季目录在入库时会被上移回剧名，这里只验证遍历层级）
func TestWalkMergeDepthTargetsSeasonLevel(t *testing.T) {
	tree := map[string][]string{
		"/TV":        {"群星/"},
		"/TV/群星":     {"第一季/", "第二季/"},
		"/TV/群星/第一季": {"01.mkv"},
		"/TV/群星/第二季": {"01.mkv"},
	}
	got := collectDepth(t, tree, "/TV", 2)
	want := []string{"/TV/群星/第一季", "/TV/群星/第二季"}
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

// 合并深度=3：条目继续下钻到季目录里的子目录
func TestWalkMergeDepthTargetsDeeperLevel(t *testing.T) {
	tree := map[string][]string{
		"/TV":           {"群星/"},
		"/TV/群星":        {"第一季/"},
		"/TV/群星/第一季":    {"01/", "02/"},
		"/TV/群星/第一季/01": {"a.mkv"},
		"/TV/群星/第一季/02": {"b.mkv"},
	}
	got := collectDepth(t, tree, "/TV", 3)
	want := []string{"/TV/群星/第一季/01", "/TV/群星/第一季/02"}
	sort.Strings(want)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %v\nwant %v", got, want)
	}
}

func TestSeasonNumberOf(t *testing.T) {
	cases := map[string]int{
		"第一季":        1,
		"第2季":        2,
		"第 3 季":      3,
		"第十季":        10,
		"第十二季":       12,
		"第二十一季":      21,
		"Season 4":   4,
		"season.05":  5,
		"S06":        6,
		"第一季.1080p":  1,
		"群星":         0,
		"S01E01":     0,
		"Specials":   0,
		"Season 1 -": 1,
	}
	for name, want := range cases {
		if got := seasonNumberOf(name); got != want {
			t.Fatalf("seasonNumberOf(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestIsSeasonDir(t *testing.T) {
	yes := []string{"第一季", "第2季", "第 3 季", "Season 1", "season.02", "S01", "s1", "第一季.1080p"}
	for _, name := range yes {
		if !isSeasonDir(name) {
			t.Fatalf("%q 应判定为季目录", name)
		}
	}
	no := []string{"群星", "S01E01", "2012", "电影", "Specials"}
	for _, name := range no {
		if isSeasonDir(name) {
			t.Fatalf("%q 不应判定为季目录", name)
		}
	}
}

// 季目录必须上移到剧名，否则同一部剧会被拆成「第一季」「第二季」多条并重复刮削
func TestPromoteSeasonTarget(t *testing.T) {
	cases := []struct {
		target    string
		root      string
		pathMerge bool
		want      string
		promoted  bool
	}{
		{"/TV/群星/第一季", "/TV", true, "/TV/群星", true},
		{"/TV/群星/Season 2", "/TV", true, "/TV/群星", true},
		{"/TV/群星", "/TV", true, "/TV/群星", false},
		// 父目录就是扫描根：不能把整个库并成一条
		{"/TV/第一季", "/TV", true, "/TV/第一季", false},
		// 非合并模式不受影响
		{"/TV/群星/第一季", "/TV", false, "/TV/群星/第一季", false},
	}
	for _, c := range cases {
		got, promoted := promoteSeasonTarget(c.target, c.root, model.MediaTypeVideo, c.pathMerge)
		if got != c.want || promoted != c.promoted {
			t.Fatalf("promoteSeasonTarget(%q) = (%q,%v), want (%q,%v)", c.target, got, promoted, c.want, c.promoted)
		}
	}
	// 音乐 / 其他类型不做此处理
	if got, promoted := promoteSeasonTarget("/音乐/专辑/第一季", "/音乐", model.MediaTypeMusic, true); got != "/音乐/专辑/第一季" || promoted {
		t.Fatalf("音乐类型不应上移，got %q promoted=%v", got, promoted)
	}
}

// 递归选集必须带相对路径：播放地址按 folder_path/file_name/选集.file_name 拼，
// 缺了「第一季/」这一层会 500/404。
func TestBuildEpisodesKeepsSubPath(t *testing.T) {
	tree := map[string][]string{
		"/TV/群星":     {"第一季/", "第二季/", "花絮/"},
		"/TV/群星/第一季": {"群星.S01E01.mp4"},
		"/TV/群星/第二季": {"群星.S02E01.mp4"},
		"/TV/群星/花絮":  {"trailer.mp4"},
	}
	raw, err := buildEpisodesFromFolderWith(context.Background(), fakeTree(tree), "/TV/群星", model.MediaTypeVideo, true)
	if err != nil {
		t.Fatal(err)
	}
	var eps []EpisodeInfo
	if err := json.Unmarshal([]byte(raw), &eps); err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 {
		t.Fatalf("episodes = %+v, want 2（花絮不计入）", eps)
	}
	if eps[0].FileName != "第一季/群星.S01E01.mp4" {
		t.Fatalf("file_name = %q, want 第一季/群星.S01E01.mp4", eps[0].FileName)
	}
	if eps[1].FileName != "第二季/群星.S02E01.mp4" {
		t.Fatalf("file_name = %q, want 第二季/群星.S02E01.mp4", eps[1].FileName)
	}
	if eps[0].Title != "第一季 / 群星.S01E01" {
		t.Fatalf("title = %q", eps[0].Title)
	}
}

// 层级还没到目标深度就没有子目录：当前目录自己作为一条，不能一条都扫不出来
func TestWalkMergeDepthFallsBackWhenTooShallow(t *testing.T) {
	tree := map[string][]string{
		"/TV":    {"剧A/"},
		"/TV/剧A": {"01.mkv"},
	}
	got := collectDepth(t, tree, "/TV", 3)
	if len(got) != 1 || got[0] != "/TV/剧A" {
		t.Fatalf("got %v, want [/TV/剧A]", got)
	}
}

// 专辑目录里混入封面 / 说明文件时：曲目仍然入库，且序号不能跳号
func TestCollectMusicEntriesSkipsNonAudio(t *testing.T) {
	entries := []model.Obj{
		&model.Object{Name: "cover.jpg"},
		&model.Object{Name: "01 - Intro.flac"},
		&model.Object{Name: "readme.txt"},
		&model.Object{Name: "CD2", IsFolder: true},
		&model.Object{Name: "02 - Outro.mp3"},
	}
	files, eps := collectMusicEntries("/music/Album", entries, model.MediaTypeMusic)
	if len(files) != 2 || len(eps) != 2 {
		t.Fatalf("files=%v eps=%v, want 2 music files", files, eps)
	}
	if eps[0].Index != 1 || eps[1].Index != 2 {
		t.Fatalf("episode index = %d,%d, want 1,2（非音频文件不能占号）", eps[0].Index, eps[1].Index)
	}
	if files[0] != "/music/Album/01 - Intro.flac" {
		t.Fatalf("files[0] = %q", files[0])
	}
	if eps[0].Title != "01 - Intro" || eps[0].FileName != "01 - Intro.flac" {
		t.Fatalf("episode = %+v", eps[0])
	}
}

func TestBuildEpisodeInfo(t *testing.T) {
	cases := []struct {
		name  string
		index int
		title string
	}{
		{"01 - 试播集.mkv", 1, "试播集"},
		{"02.别对我撒谎.mp4", 2, "别对我撒谎"},
		{"第01集.mkv", 0, "第01集"},   // 中文序号不在解析范围内，保留原名
		{"1080p.mkv", 0, "1080p"}, // 分辨率不能被当成集号
		{"12.mp4", 0, "12"},       // 整个名字都是数字时没有标题部分
	}
	for _, c := range cases {
		ep := buildEpisodeInfo(c.name)
		if ep.Index != c.index || ep.Title != c.title {
			t.Errorf("buildEpisodeInfo(%q) = {index:%d title:%q}, want {index:%d title:%q}",
				c.name, ep.Index, ep.Title, c.index, c.title)
		}
		if ep.FileName != c.name {
			t.Errorf("buildEpisodeInfo(%q).FileName = %q", c.name, ep.FileName)
		}
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
