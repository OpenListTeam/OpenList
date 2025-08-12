package strm

func supportSuffix() map[string]struct{} {
	return map[string]struct{}{
		// video
		"mp4":  {},
		"mkv":  {},
		"flv":  {},
		"avi":  {},
		"wmv":  {},
		"ts":   {},
		"rmvb": {},
		"webm": {},
		// audio
		"mp3":  {},
		"flac": {},
		"aac":  {},
		"wav":  {},
		"ogg":  {},
		"m4a":  {},
		"wma":  {},
		"alac": {},
	}
}

func downloadSuffix() map[string]struct{} {
	return map[string]struct{}{
		// strm
		"strm": {},
		// subtitles
		"ass": {},
		"srt": {},
		"vtt": {},
		"sub": {},
	}
}
