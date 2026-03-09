package media

import (
	"encoding/binary"
	"io"
	"strconv"
	"strings"
)

// MusicTag 音频文件标签信息
type MusicTag struct {
	Title       string // TIT2
	Artist      string // TPE1
	Album       string // TALB
	AlbumArtist string // TPE2
	TrackNumber int    // TRCK
	Year        string // TYER / TDRC
	Genre       string // TCON
	CoverData   []byte // 封面图片原始字节（APIC / PICTURE）
	CoverMIME   string // 封面图片 MIME 类型（如 image/jpeg）
}

// ParseID3v2 从 io.Reader 中解析 ID3v2 标签（只读取文件头部，不需要 Seek）
// 支持 ID3v2.3 和 ID3v2.4
func ParseID3v2(r io.Reader) (*MusicTag, error) {
	// 读取 ID3v2 头部（10 字节）
	header := make([]byte, 10)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}

	// 检查魔数
	if string(header[0:3]) != "ID3" {
		return nil, nil // 不是 ID3v2 文件，不报错
	}

	version := header[3] // 主版本号：3 = ID3v2.3, 4 = ID3v2.4
	if version < 3 || version > 4 {
		return nil, nil // 只支持 v2.3 和 v2.4
	}

	// 解析标签总大小（syncsafe integer，4 字节，每字节最高位为 0）
	tagSize := syncsafeToInt(header[6:10])
	if tagSize <= 0 || tagSize > 10*1024*1024 { // 最大 10MB
		return nil, nil
	}

	// 读取所有帧数据
	data := make([]byte, tagSize)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, nil // 读取失败时静默跳过
	}

	tag := &MusicTag{}
	pos := 0

	// 跳过扩展头部（如果有）
	flags := header[5]
	if flags&0x40 != 0 { // 有扩展头部
		if pos+4 > len(data) {
			return tag, nil
		}
		extSize := int(binary.BigEndian.Uint32(data[pos : pos+4]))
		pos += extSize
	}

	// 解析帧
	for pos+10 <= len(data) {
		frameID := string(data[pos : pos+4])
		if frameID == "\x00\x00\x00\x00" {
			break // 填充区域，结束
		}

		frameSize := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		pos += 10 // 跳过帧头（4+4+2）

		if frameSize <= 0 || pos+frameSize > len(data) {
			break
		}

		frameData := data[pos : pos+frameSize]
		pos += frameSize

		// 解析文本帧（T 开头的帧）
		if len(frameID) == 4 && frameID[0] == 'T' && len(frameData) > 0 {
			text := decodeID3Text(frameData)
			switch frameID {
			case "TIT2":
				tag.Title = text
			case "TPE1":
				tag.Artist = text
			case "TALB":
				tag.Album = text
			case "TPE2":
				tag.AlbumArtist = text
			case "TRCK":
				// 格式可能是 "1" 或 "1/12"
				parts := strings.SplitN(text, "/", 2)
				if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
					tag.TrackNumber = n
				}
			case "TYER", "TDRC":
				if tag.Year == "" {
					tag.Year = text
				}
			case "TCON":
				tag.Genre = parseID3Genre(text)
			}
		}

		// 解析 APIC 帧（内嵌封面图片），只取第一张
		if frameID == "APIC" && tag.CoverData == nil && len(frameData) > 1 {
			tag.CoverData, tag.CoverMIME = parseAPICFrame(frameData)
		}
	}

	return tag, nil
}

// parseAPICFrame 解析 ID3v2 APIC 帧，返回图片字节和 MIME 类型
// APIC 格式：编码字节(1) + MIME字符串(null结尾) + 图片类型(1) + 描述字符串(null结尾) + 图片数据
func parseAPICFrame(data []byte) ([]byte, string) {
	if len(data) < 4 {
		return nil, ""
	}
	// encoding := data[0]  // 文本编码（只影响描述字段，MIME 始终是 ASCII）
	pos := 1

	// 读取 MIME 类型（null 结尾的 ASCII 字符串）
	nullIdx := -1
	for i := pos; i < len(data); i++ {
		if data[i] == 0 {
			nullIdx = i
			break
		}
	}
	if nullIdx < 0 {
		return nil, ""
	}
	mimeType := string(data[pos:nullIdx])
	pos = nullIdx + 1

	if pos >= len(data) {
		return nil, ""
	}
	// 图片类型（1字节，3 = Cover (front)，但我们取任意第一张）
	pos++ // 跳过图片类型字节

	// 跳过描述字符串（null 结尾，编码由第一字节决定）
	encoding := data[0]
	if encoding == 0x01 || encoding == 0x02 {
		// UTF-16：找 \x00\x00 结尾
		for pos+1 < len(data) {
			if data[pos] == 0 && data[pos+1] == 0 {
				pos += 2
				break
			}
			pos += 2
		}
	} else {
		// ISO-8859-1 / UTF-8：找单个 \x00 结尾
		for pos < len(data) {
			if data[pos] == 0 {
				pos++
				break
			}
			pos++
		}
	}

	if pos >= len(data) {
		return nil, ""
	}

	// 标准化 MIME 类型
	if mimeType == "" || mimeType == "image/" {
		mimeType = "image/jpeg" // 默认
	}

	imgData := make([]byte, len(data)-pos)
	copy(imgData, data[pos:])
	return imgData, mimeType
}

// syncsafeToInt 将 syncsafe integer 转换为普通整数
func syncsafeToInt(b []byte) int {
	result := 0
	for _, v := range b {
		result = (result << 7) | int(v&0x7F)
	}
	return result
}

// decodeID3Text 解码 ID3 文本帧（第一字节是编码标识）
func decodeID3Text(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	encoding := data[0]
	content := data[1:]

	// 去掉末尾的 null 字节
	switch encoding {
	case 0x00: // ISO-8859-1
		// 去掉末尾 null
		content = trimNull(content, false)
		// 尝试 UTF-8 解码，如果失败则按 Latin-1 处理
		return latin1ToUTF8(content)
	case 0x01: // UTF-16 with BOM
		content = trimNull(content, true)
		return utf16ToUTF8(content)
	case 0x02: // UTF-16 BE without BOM
		content = trimNull(content, true)
		return utf16BEToUTF8(content)
	case 0x03: // UTF-8
		content = trimNull(content, false)
		return string(content)
	default:
		return string(content)
	}
}

// trimNull 去掉末尾的 null 字节
func trimNull(b []byte, wide bool) []byte {
	if wide {
		// UTF-16: 去掉末尾的 \x00\x00
		for len(b) >= 2 && b[len(b)-2] == 0 && b[len(b)-1] == 0 {
			b = b[:len(b)-2]
		}
	} else {
		for len(b) > 0 && b[len(b)-1] == 0 {
			b = b[:len(b)-1]
		}
	}
	return b
}

// latin1ToUTF8 将 Latin-1 编码转换为 UTF-8
func latin1ToUTF8(b []byte) string {
	runes := make([]rune, len(b))
	for i, v := range b {
		runes[i] = rune(v)
	}
	return string(runes)
}

// utf16ToUTF8 将 UTF-16（带 BOM）转换为 UTF-8
func utf16ToUTF8(b []byte) string {
	if len(b) < 2 {
		return ""
	}
	var bigEndian bool
	if b[0] == 0xFF && b[1] == 0xFE {
		bigEndian = false
		b = b[2:]
	} else if b[0] == 0xFE && b[1] == 0xFF {
		bigEndian = true
		b = b[2:]
	}
	return decodeUTF16(b, bigEndian)
}

// utf16BEToUTF8 将 UTF-16 BE（无 BOM）转换为 UTF-8
func utf16BEToUTF8(b []byte) string {
	return decodeUTF16(b, true)
}

// decodeUTF16 解码 UTF-16 字节序列
func decodeUTF16(b []byte, bigEndian bool) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	runes := make([]rune, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		var code uint16
		if bigEndian {
			code = uint16(b[i])<<8 | uint16(b[i+1])
		} else {
			code = uint16(b[i+1])<<8 | uint16(b[i])
		}
		runes = append(runes, rune(code))
	}
	return string(runes)
}

// parseID3Genre 解析 ID3 流派字段（可能是 "(17)" 格式的数字引用）
func parseID3Genre(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 2 && s[0] == '(' && s[len(s)-1] == ')' {
		// 数字引用格式，直接返回原始字符串
		return s
	}
	return s
}

// ─── FLAC Vorbis Comment 解析 ────────────────────────────────────────────────

// ParseFLACVorbisComment 从 io.Reader 中解析 FLAC 文件的 Vorbis Comment 元数据
// FLAC 格式：4字节魔数 "fLaC" + 若干 METADATA_BLOCK
// METADATA_BLOCK：1字节(最高位=是否最后块, 低7位=块类型) + 3字节长度 + 数据
// 块类型 4 = VORBIS_COMMENT
func ParseFLACVorbisComment(r io.Reader) (*MusicTag, error) {
	// 读取魔数（4字节）
	magic := make([]byte, 4)
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if string(magic) != "fLaC" {
		return nil, nil // 不是 FLAC 文件
	}

	// 遍历 METADATA_BLOCK，找到 VORBIS_COMMENT（类型4）和 PICTURE（类型6）
	var tag *MusicTag
	for {
		// 读取块头（4字节：1字节标志+类型 + 3字节长度）
		blockHeader := make([]byte, 4)
		if _, err := io.ReadFull(r, blockHeader); err != nil {
			return nil, nil // 读取失败，静默跳过
		}

		isLast := blockHeader[0]&0x80 != 0
		blockType := blockHeader[0] & 0x7F
		blockLen := int(blockHeader[1])<<16 | int(blockHeader[2])<<8 | int(blockHeader[3])

		if blockLen < 0 || blockLen > 16*1024*1024 { // 最大 16MB
			return nil, nil
		}

		if blockType == 4 {
			// VORBIS_COMMENT 块
			data := make([]byte, blockLen)
			if _, err := io.ReadFull(r, data); err != nil {
				return nil, nil
			}
			tag = parseVorbisCommentData(data)
			if isLast {
				return tag, nil
			}
			// 继续读取后续块，寻找 PICTURE 块（类型6）
			continue
		}

		if blockType == 6 {
			// PICTURE 块（FLAC 内嵌封面）
			data := make([]byte, blockLen)
			if _, err := io.ReadFull(r, data); err == nil && tag != nil && tag.CoverData == nil {
				tag.CoverData, tag.CoverMIME = parseFLACPictureBlock(data)
			} else if err != nil {
				// 读取失败，跳过
				_ = err
			}
			if isLast {
				return tag, nil
			}
			continue
		}

		// 跳过此块
		if _, err := io.CopyN(io.Discard, r, int64(blockLen)); err != nil {
			return nil, nil
		}

		if isLast {
			break
		}
	}

	return tag, nil
}

// parseFLACPictureBlock 解析 FLAC PICTURE 元数据块，返回图片字节和 MIME 类型
// 格式（大端序）：
//   4字节 picture_type
//   4字节 mime_length + mime_string
//   4字节 description_length + description_string
//   4字节 width, 4字节 height, 4字节 color_depth, 4字节 color_count
//   4字节 data_length + data
func parseFLACPictureBlock(data []byte) ([]byte, string) {
	if len(data) < 8 {
		return nil, ""
	}
	pos := 4 // 跳过 picture_type

	// 读取 MIME 类型
	if pos+4 > len(data) {
		return nil, ""
	}
	mimeLen := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 4
	if pos+mimeLen > len(data) {
		return nil, ""
	}
	mimeType := string(data[pos : pos+mimeLen])
	pos += mimeLen

	// 跳过描述字符串
	if pos+4 > len(data) {
		return nil, ""
	}
	descLen := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 4 + descLen

	// 跳过 width(4) + height(4) + color_depth(4) + color_count(4)
	pos += 16

	// 读取图片数据
	if pos+4 > len(data) {
		return nil, ""
	}
	dataLen := int(binary.BigEndian.Uint32(data[pos : pos+4]))
	pos += 4
	if pos+dataLen > len(data) {
		return nil, ""
	}

	if mimeType == "" {
		mimeType = "image/jpeg"
	}

	imgData := make([]byte, dataLen)
	copy(imgData, data[pos:pos+dataLen])
	return imgData, mimeType
}

// parseVorbisCommentData 解析 Vorbis Comment 数据块
// 格式（小端序）：
//   4字节 vendor_length + vendor_string
//   4字节 comment_count
//   每条注释：4字节长度 + UTF-8字符串（格式 "KEY=VALUE"）
func parseVorbisCommentData(data []byte) *MusicTag {
	tag := &MusicTag{}
	pos := 0

	// 跳过 vendor string
	if pos+4 > len(data) {
		return tag
	}
	vendorLen := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
	pos += 4 + vendorLen

	// 读取注释数量
	if pos+4 > len(data) {
		return tag
	}
	commentCount := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
	pos += 4

	for i := 0; i < commentCount; i++ {
		if pos+4 > len(data) {
			break
		}
		commentLen := int(binary.LittleEndian.Uint32(data[pos : pos+4]))
		pos += 4

		if commentLen <= 0 || pos+commentLen > len(data) {
			break
		}

		comment := string(data[pos : pos+commentLen])
		pos += commentLen

		// 解析 "KEY=VALUE" 格式（大小写不敏感）
		eqIdx := strings.IndexByte(comment, '=')
		if eqIdx < 0 {
			continue
		}
		key := strings.ToUpper(comment[:eqIdx])
		value := strings.TrimSpace(comment[eqIdx+1:])
		if value == "" {
			continue
		}

		switch key {
		case "TITLE":
			tag.Title = value
		case "ARTIST":
			if tag.Artist == "" {
				tag.Artist = value
			}
		case "ALBUM":
			tag.Album = value
		case "ALBUMARTIST", "ALBUM ARTIST":
			tag.AlbumArtist = value
		case "TRACKNUMBER", "TRACK":
			// 格式可能是 "1" 或 "1/12"
			parts := strings.SplitN(value, "/", 2)
			if n, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil {
				tag.TrackNumber = n
			}
		case "DATE", "YEAR":
			if tag.Year == "" && len(value) >= 4 {
				tag.Year = value[:4]
			}
		case "GENRE":
			tag.Genre = value
		}
	}

	return tag
}
