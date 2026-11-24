package db

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// 重扫不能把 video_type 清空：扫描路径没配类型标签时扫描器写入空串，
// 若直接覆盖会把 TMDB 刮削出的 movie/tv 抹掉，Emby 里的分类跟着翻转。
func TestCreateOrUpdateMediaItemKeepsVideoType(t *testing.T) {
	db = openTestDB(t, "ol_")
	if err := db.AutoMigrate(&model.MediaItem{}); err != nil {
		t.Fatal(err)
	}

	scrapedAt := time.Now()
	first := &model.MediaItem{
		MediaType:  model.MediaTypeVideo,
		FolderPath: "/tv/show",
		FileName:   "show",
		IsFolder:   true,
		VideoType:  "tv",
		ScrapedAt:  &scrapedAt,
	}
	if err := CreateOrUpdateMediaItem(first); err != nil {
		t.Fatal(err)
	}

	// 重扫：类型标签无法判定 → VideoType 为空，应保留原值
	rescan := &model.MediaItem{
		MediaType:  model.MediaTypeVideo,
		FolderPath: "/tv/show",
		FileName:   "show",
		IsFolder:   true,
	}
	if err := CreateOrUpdateMediaItem(rescan); err != nil {
		t.Fatal(err)
	}
	got, err := GetMediaItemByID(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoType != "tv" {
		t.Fatalf("video_type = %q after rescan, want %q", got.VideoType, "tv")
	}

	// 标签能判定时以标签为准（用户改了类型标签要能生效）
	retag := &model.MediaItem{
		MediaType:  model.MediaTypeVideo,
		FolderPath: "/tv/show",
		FileName:   "show",
		IsFolder:   true,
		VideoType:  "movie",
	}
	if err := CreateOrUpdateMediaItem(retag); err != nil {
		t.Fatal(err)
	}
	got, err = GetMediaItemByID(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.VideoType != "movie" {
		t.Fatalf("video_type = %q after retag, want %q", got.VideoType, "movie")
	}
}
