package transcode

import "strings"

// ffmpeg / ffprobe 支持 concat:、subfile:、data: 等大量协议。
// SourceURL 最终来自请求中的路径或 Host 头，若不限制协议，攻击者就可能借 ffmpeg
// 读取服务器本地文件或访问内网地址。这里按输入类型收敛到最小协议集。

const (
	// protocolWhitelistLocal 本地文件输入只允许 file 协议
	protocolWhitelistLocal = "file"
	// protocolWhitelistRemote 远程输入允许 HTTP(S) 及其底层传输；仍然禁止 concat/subfile/data 等
	protocolWhitelistRemote = "file,http,https,tcp,tls,crypto"
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
	// 其它带 scheme 的形式（如 concat:、subfile:、data:、pipe:、rtmp://）一律拒绝。
	// Windows 盘符路径（C:\ 或 C:/）不是 scheme，需要放行。
	if idx := strings.Index(s, ":"); idx > 1 {
		return false
	}
	if idx := strings.Index(s, ":"); idx == 1 {
		// 单字母 + 冒号：必须是盘符路径
		if len(s) < 3 || (s[2] != '\\' && s[2] != '/') {
			return false
		}
	}
	return true
}
