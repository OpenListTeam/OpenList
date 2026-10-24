package transcode

import "strings"

// ffmpeg / ffprobe 支持 concat:、subfile:、data: 等大量协议。
// SourceURL 最终来自请求中的路径或 Host 头，若不限制协议，攻击者就可能借 ffmpeg
// 读取服务器本地文件或访问内网地址。这里按输入类型收敛到最小协议集。

const (
	// protocolWhitelistLocal 本地文件输入只允许 file 协议
	protocolWhitelistLocal = "file"
	// protocolWhitelistRemote 远程输入允许 HTTP(S) 及其底层传输；禁止 concat/subfile/data/pipe，
	// 同时去掉 file 与 crypto：HTTP 源用不到它们，留着等于给被构造的播放列表留了读本地文件的通道。
	protocolWhitelistRemote = "http,https,tcp,tls"
)

// ProtocolWhitelistFor 根据输入地址返回应传给 ffmpeg/ffprobe 的 -protocol_whitelist 值。
func ProtocolWhitelistFor(source string) string {
	s := strings.ToLower(strings.TrimSpace(source))
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return protocolWhitelistRemote
	}
	return protocolWhitelistLocal
}

// ValidSourceInput 判断输入是否可以安全交给 ffmpeg：
// 只接受绝对本地路径或 http(s) URL，拒绝 concat:/subfile:/data:/pipe: 等协议前缀，
// 也拒绝以 - 开头（会被当作 ffmpeg 选项）的值。
func ValidSourceInput(source string) bool {
	s := strings.TrimSpace(source)
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "http://") || strings.HasPrefix(low, "https://") {
		return true
	}
	// 本地输入只接受绝对路径。
	// 不能按「冒号位置」判 scheme：文件名里带冒号非常常见
	// （/mnt/media/Star Wars: Episode IV.mkv），那样会被误判成协议前缀。
	// concat:/subfile:/data: 这些前缀都不是绝对路径，且还会被 -protocol_whitelist 挡住。
	// 注意不能用 filepath.IsAbs：它是平台相关的，Windows 上会拒绝 "/data/a.mkv"
	// 这类 VFS 绝对路径（OpenList 的路径始终是正斜杠开头）。
	return strings.HasPrefix(s, "/") || isWindowsDrivePath(s)
}

// isWindowsDrivePath 判断 C:\ 或 C:/ 形式的盘符绝对路径。
// 跨平台使用：在 Linux 上构建时会话也可能处理 Windows 风格路径。
func isWindowsDrivePath(s string) bool {
	if len(s) < 3 || s[1] != ':' {
		return false
	}
	c := s[0]
	isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
	return isLetter && (s[2] == '\\' || s[2] == '/')
}
