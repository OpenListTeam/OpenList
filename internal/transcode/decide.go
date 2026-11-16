package transcode

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
)

// DecideRequest 决策器输入
type DecideRequest struct {
	FileName string
	FileSize int64       // 字节
	Probe    SourceProbe // 可选；为空则只用大小+后缀判断
}

// DecideResult 决策结果
type DecideResult struct {
	NeedTranscode bool
	Reason        string
}

// Decide 根据全局设置判断是否需要走转码
func Decide(r DecideRequest) DecideResult {
	if !setting.GetBool(conf.TranscodeEnabled) {
		return DecideResult{false, "transcode disabled"}
	}
	// 1. 大小阈值
	minGB := setting.GetInt(conf.TranscodeMinSizeGB, 5)
	if minGB > 0 {
		minBytes := int64(minGB) * 1024 * 1024 * 1024
		if r.FileSize > 0 && r.FileSize < minBytes {
			return DecideResult{false, "size below threshold"}
		}
	}
	// 2. 后缀过滤
	exts := splitCSV(setting.GetStr(conf.TranscodeSourceExtensions))
	if len(exts) > 0 {
		ext := strings.ToLower(strings.TrimPrefix(getExt(r.FileName), "."))
		if !containsFold(exts, ext) {
			return DecideResult{false, "extension not in list"}
		}
	}
	// 3. 编码过滤（仅当 probe 提供时）
	codecs := splitCSV(setting.GetStr(conf.TranscodeSourceCodecs))
	if r.Probe.VideoCodec != "" && len(codecs) > 0 {
		if !containsFold(codecs, r.Probe.VideoCodec) {
			return DecideResult{false, "codec not in list"}
		}
	}
	// 4. 码率过滤（仅当 probe 提供时）
	minBR := setting.GetInt(conf.TranscodeMinBitrateMbps, 0)
	if minBR > 0 && r.Probe.VideoBitrate > 0 {
		if r.Probe.VideoBitrate < int64(minBR)*1_000_000 {
			return DecideResult{false, "bitrate below threshold"}
		}
	}
	return DecideResult{true, "ok"}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func containsFold(arr []string, v string) bool {
	for _, a := range arr {
		if strings.EqualFold(a, v) {
			return true
		}
	}
	return false
}

func getExt(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	return name[i:]
}

// BuildProfile 根据当前全局设置构建默认 Profile
func BuildProfile() Profile {
	res := setting.GetStr(conf.TranscodeOutputResolution)
	scale := ""
	name := "default"
	switch res {
	case "source":
		scale = ""
		name = "source"
	case "3840x2160":
		scale = "3840:-2"
		name = "2160p"
	case "2560x1440":
		scale = "2560:-2"
		name = "1440p"
	case "1920x1080":
		scale = "1920:-2"
		name = "1080p"
	case "1280x720":
		scale = "1280:-2"
		name = "720p"
	case "854x480":
		scale = "854:-2"
		name = "480p"
	}
	return Profile{
		Name:         name,
		VideoCodec:   setting.GetStr(conf.TranscodeOutputCodec),
		VideoBitrate: setting.GetStr(conf.TranscodeOutputBitrate),
		Scale:        scale,
		AudioCodec:   setting.GetStr(conf.TranscodeOutputAudioCodec),
		AudioBitrate: setting.GetStr(conf.TranscodeOutputAudioBitrate),
		HWAccel:      setting.GetStr(conf.TranscodeHWAccel),
	}
}

// BuildOutputSpec 根据当前全局设置构建输出规格
func BuildOutputSpec() OutputSpec {
	return OutputSpec{
		Format:          OutputFormat(setting.GetStr(conf.TranscodeOutputFormat)),
		SegmentDuration: setting.GetInt(conf.TranscodeSegmentDuration, 6),
	}
}
