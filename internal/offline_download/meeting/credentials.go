package meeting

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// platform 对平台域名做完整边界匹配，防止把登录 Cookie 发给仿冒域名。
func platform(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if (host == "feishu.cn" || strings.HasSuffix(host, ".feishu.cn")) && strings.HasPrefix(u.Path, "/minutes/") {
		return "feishu"
	}
	if host == "meeting.tencent.com" && strings.HasPrefix(u.Path, "/cw/") {
		return "tencent"
	}
	return ""
}

// ValidateBatch 只允许支持的分享页或直接媒体 URL。Cookie 是可选值；
// 有 Cookie 时整批必须属于同一平台/源站，在入队前一次校验，避免部分提交。
func ValidateBatch(urls []string, cookie string) error {
	if len(cookie) > 32<<10 || strings.ContainsAny(cookie, "\r\n\x00") {
		return fmt.Errorf("Cookie 格式无效或超过 32 KiB")
	}
	if strings.TrimSpace(cookie) != "" {
		r := http.Request{Header: http.Header{"Cookie": {normalizeCookie(cookie)}}}
		if len(r.Cookies()) == 0 {
			return fmt.Errorf("Cookie 格式无效，请填写浏览器 Cookie 请求头")
		}
	}
	scope := ""
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		u, err := mediaURL(raw)
		if err != nil {
			return err
		}
		current := platform(raw)
		if current == "" {
			ext := strings.ToLower(filepath.Ext(u.Path))
			if ext != ".mp4" && ext != ".m3u8" {
				return fmt.Errorf("仅支持飞书妙记、腾讯会议分享链接或 MP4/m3u8 直链")
			}
			current = u.Scheme + "://" + strings.ToLower(u.Host)
		}
		if cookie != "" && scope != "" && scope != current {
			return fmt.Errorf("一次只能提交同一平台或源站的链接与 Cookie")
		}
		scope = current
	}
	return nil
}
func normalizeCookie(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "cookie:") {
		raw = strings.TrimSpace(raw[7:])
	}
	return raw
}

var keyMu sync.Mutex

// 加密密钥只属于本工具，队列中的密文绑定任务 ID，不能跨任务替换。
// 必须随 OpenList 数据卷备份；解密时不生成新密钥。
func credentialKey(create bool) ([]byte, error) {
	keyMu.Lock()
	defer keyMu.Unlock()
	p := filepath.Join(conf.DataDir, "meeting-video", "cookie.key")
	key, err := os.ReadFile(p)
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("会议 Cookie 密钥损坏")
		}
		return key, nil
	}
	if !os.IsNotExist(err) || !create {
		return nil, fmt.Errorf("会议 Cookie 密钥不可用，请检查原数据卷")
	}
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.Write(key)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	return key, closeErr
}
func sealCookie(id, plain string) (string, error) {
	key, err := credentialKey(true)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := make([]byte, aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(normalizeCookie(plain)), []byte(id))), nil
}
func unsealCookie(id, sealed string) (string, error) {
	if sealed == "" {
		return "", nil
	}
	key, err := credentialKey(false)
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("会议 Cookie 凭据无效，请重新提交")
	}
	plain, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], []byte(id))
	if err != nil {
		return "", fmt.Errorf("会议 Cookie 解密失败，请重新提交")
	}
	return string(plain), nil
}
