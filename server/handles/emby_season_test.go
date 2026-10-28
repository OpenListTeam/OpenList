package handles

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// 规则必须和前端 episodeSeasonOf 一致，否则老数据在 Emby 和网页端会分到不同季
func TestSeasonFromText(t *testing.T) {
	cases := map[string]int{
		"第一季 / 群星.S01E01": 1,
		"第二季 / 群星.S02E01": 2,
		"第十二季 / 正片":       12,
		"第二十季":            20,
		"Season 3":        3,
		"season.02":       2,
		"season-4":        4,
		"Show.S05E09":     5,
		"群星.S01E01.mkv":   1,
		"群星 第一季":          1,
		"正片.mkv":          0,
		"":                0,
	}
	for text, want := range cases {
		if got := seasonFromText(text); got != want {
			t.Fatalf("seasonFromText(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestEpisodeSeasonOfPrecedence(t *testing.T) {
	item := &model.MediaItem{Season: 2}
	// 选集自带 season 最准
	if got := episodeSeasonOf(item, model.EpisodeInfo{Season: 5, Title: "第一季 / x"}); got != 5 {
		t.Fatalf("ep.season 应优先，got %d", got)
	}
	// 其次条目级 season（单文件剧集）
	if got := episodeSeasonOf(item, model.EpisodeInfo{Title: "第一季 / x"}); got != 2 {
		t.Fatalf("item.season 应优先于标题，got %d", got)
	}
	// 都没有时按标题推断（老数据）
	if got := episodeSeasonOf(&model.MediaItem{}, model.EpisodeInfo{Title: "第三季 / x"}); got != 3 {
		t.Fatalf("应按标题推断第 3 季，got %d", got)
	}
	// 标题没有季信息时按文件名推断
	if got := episodeSeasonOf(&model.MediaItem{}, model.EpisodeInfo{FileName: "Show.S04E01.mkv"}); got != 4 {
		t.Fatalf("应按文件名推断第 4 季，got %d", got)
	}
	// 完全无法判断时归入第 1 季
	if got := episodeSeasonOf(&model.MediaItem{}, model.EpisodeInfo{FileName: "正片.mkv"}); got != 1 {
		t.Fatalf("无法判断时应为第 1 季，got %d", got)
	}
}
