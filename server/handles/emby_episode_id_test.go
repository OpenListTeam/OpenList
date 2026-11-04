package handles

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// 新格式必须带季号；旧的 ep-<item>-<num> 仍要能解析（已缓存的客户端 ID 不能直接失效）
func TestParseEpisodeID(t *testing.T) {
	cases := []struct {
		id     string
		item   uint
		season int
		num    int
		ok     bool
	}{
		{"ep-12-2-5", 12, 2, 5, true},
		{"ep-12-5", 12, 0, 5, true},
		{"ep-12", 0, 0, 0, false},
		{"ep-abc-1", 0, 0, 0, false},
		// season=0 与旧格式等价，按「任意季」匹配，不做拒收
		{"ep-12-0-5", 12, 0, 5, true},
		{"mv-12", 0, 0, 0, false},
		{"ep-12-1-x", 0, 0, 0, false},
	}
	for _, c := range cases {
		item, season, num, ok := parseEpisodeID(c.id)
		if ok != c.ok || item != c.item || season != c.season || num != c.num {
			t.Fatalf("parseEpisodeID(%q) = (%d,%d,%d,%v), want (%d,%d,%d,%v)",
				c.id, item, season, num, ok, c.item, c.season, c.num, c.ok)
		}
	}
}

func newResumeTestLibrary(episodes string) (*embyLibrary, uint) {
	item := model.MediaItem{
		IsFolder:   true,
		FolderPath: "/TV",
		FileName:   "群星",
		Episodes:   episodes,
	}
	item.ID = 7
	return &embyLibrary{videos: []model.MediaItem{item}}, 7
}

// 同号集出现在不同季时，集号无法反推季号 → 退回旧的两段 ID（行为与改动前一致）
func TestResumeEpisodeIDAmbiguousSeason(t *testing.T) {
	lib, id := newResumeTestLibrary(`[
		{"file_name":"01.mkv","index":1,"season":1},
		{"file_name":"01.mkv","index":1,"season":2}
	]`)
	if got := lib.resumeEpisodeID(id, 1); got != "ep-7-1" {
		t.Fatalf("同号集应退回旧格式，got %q", got)
	}
}

// 能唯一定位时补上季号，否则 S02 的续播会被算到 S01 的同号集上
func TestResumeEpisodeIDUniqueSeason(t *testing.T) {
	lib, id := newResumeTestLibrary(`[
		{"file_name":"01.mkv","index":1,"season":1},
		{"file_name":"02.mkv","index":2,"season":2}
	]`)
	if got := lib.resumeEpisodeID(id, 2); got != "ep-7-2-2" {
		t.Fatalf("应补上季号，got %q", got)
	}
}

// 非文件夹条目（单文件电影）沿用旧格式
func TestResumeEpisodeIDSingleFile(t *testing.T) {
	item := model.MediaItem{FolderPath: "/电影", FileName: "x.mkv"}
	item.ID = 9
	lib := &embyLibrary{videos: []model.MediaItem{item}}
	if got := lib.resumeEpisodeID(9, 1); got != "ep-9-1" {
		t.Fatalf("单文件条目应保持旧格式，got %q", got)
	}
}

func TestRegisterEpisodeIDLegacyAliases(t *testing.T) {
	lib, _ := newResumeTestLibrary(`[
		{"file_name":"S01E01.mkv","index":1,"season":1},
		{"file_name":"S02E01.mkv","index":1,"season":2},
		{"file_name":"S02E02.mkv","index":2,"season":2}
	]`)
	lib.ids = make(map[string]string)
	lib.registerIDs()
	for _, orig := range []string{"ep-7-1", "ep-7-2", "ep-7-1-1", "ep-7-2-1", "ep-7-2-2"} {
		for _, id := range []string{orig, embyNumericID(orig), embyGUID(orig)} {
			if got := lib.origID(id); got != orig {
				t.Fatalf("origID(%q) = %q, want %q", id, got, orig)
			}
			item, season, num, ok := parseEpisodeID(lib.origID(id))
			wantItem, wantSeason, wantNum, wantOK := parseEpisodeID(orig)
			if !ok || !wantOK || item != wantItem || season != wantSeason || num != wantNum {
				t.Fatalf("episode alias %q did not resolve to %q", id, orig)
			}
		}
	}
}
