package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// FillMusicFromLocal 在刮削前/失败时，使用音乐文件本身的元数据（ID3 / Vorbis Comment）
// 以及同目录 .lrc 歌词，**只填充 item 中为空的字段**。
//
// 适用场景：
//  1. 用户清空刮削后重新刮削，原本由扫描阶段填入的本地数据（封面/歌词等）已丢失
//  2. 在线刮削失败，退而求其次用文件本地信息补全
//
// 行为：
//   - is_folder=true 的合并文件夹模式：使用 episodes 中第一首音乐文件读取
//   - is_folder=false：使用 folder_path/file_name 直接读取
//
// 错误以静默忽略为主——本地兜底失败不应阻塞主流程。
func FillMusicFromLocal(ctx context.Context, item *model.MediaItem) {
	if item == nil || item.MediaType != model.MediaTypeMusic {
		return
	}

	musicPath := pickMusicFilePath(item)
	if musicPath == "" {
		return
	}

	// 解析 tag（mp3 / flac / 其它）
	ext := strings.ToLower(stdpath.Ext(musicPath))
	var tag *MusicTag
	readCtx, readCancel := context.WithTimeout(ctx, 15*time.Second)
	if reader := FetchFileReader(readCtx, musicPath); reader != nil {
		switch ext {
		case ".flac":
			tag, _ = ParseFLACVorbisComment(reader)
		default:
			tag, _ = ParseID3v2(reader)
		}
		_ = reader.Close()
	}
	readCancel()

	// 应用 tag 到空字段
	if tag != nil {
		if item.AlbumName == "" && tag.Album != "" {
			item.AlbumName = tag.Album
		}
		if item.AlbumArtist == "" {
			if tag.AlbumArtist != "" {
				item.AlbumArtist = tag.AlbumArtist
			} else if tag.Artist != "" {
				item.AlbumArtist = tag.Artist
			}
		}
		if item.ScrapedName == "" {
			if tag.Title != "" {
				item.ScrapedName = tag.Title
			} else if item.AlbumName != "" {
				item.ScrapedName = item.AlbumName
			}
		}
		if item.TrackNumber == 0 && tag.TrackNumber > 0 {
			item.TrackNumber = tag.TrackNumber
		}
		if item.ReleaseDate == "" && len(tag.Year) >= 4 {
			item.ReleaseDate = tag.Year[:4] + "-01-01"
		}
		if item.Genre == "" && tag.Genre != "" {
			item.Genre = tag.Genre
		}
		if item.Authors == "" && tag.Artist != "" {
			if b, err := json.Marshal([]string{tag.Artist}); err == nil {
				item.Authors = string(b)
			}
		}
		if item.Cover == "" && len(tag.CoverData) > 0 {
			mimeType := tag.CoverMIME
			if mimeType == "" {
				mimeType = "image/jpeg"
			}
			item.Cover = "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(tag.CoverData)
		}
	}

	// 歌词：若为空则尝试 .lrc 同名文件 / 内嵌歌词
	if item.Lyrics == "" {
		if lrc := loadLyricsForMusic(ctx, musicPath, tag); lrc != "" {
			item.Lyrics = lrc
		}
	}
}

// pickMusicFilePath 从 MediaItem 推断出可读取的音乐文件 VFS 路径
// 合并模式 (is_folder=true): 取 episodes 中第一首；解析失败回退到 folder_path/file_name
// 普通模式: folder_path/file_name
func pickMusicFilePath(item *model.MediaItem) string {
	if item == nil {
		return ""
	}
	if item.IsFolder {
		// episodes 是 JSON 数组：[{file_name,index,title,...}]
		if item.Episodes != "" {
			var eps []EpisodeInfo
			if err := json.Unmarshal([]byte(item.Episodes), &eps); err == nil && len(eps) > 0 {
				// 文件夹模式下 folder_path 是扫描根，file_name 是文件夹名
				return stdpath.Join(item.FolderPath, item.FileName, eps[0].FileName)
			}
		}
		// 回退：尝试列出文件夹内第一首音乐
		folder := stdpath.Join(item.FolderPath, item.FileName)
		if entries, err := fs.List(context.Background(), folder, &fs.ListArgs{NoLog: true}); err == nil {
			for _, e := range entries {
				if !e.IsDir() && isMediaFile(e.GetName(), model.MediaTypeMusic) {
					return stdpath.Join(folder, e.GetName())
				}
			}
		}
		return ""
	}
	return stdpath.Join(item.FolderPath, item.FileName)
}
