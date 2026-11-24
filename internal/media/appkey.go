package media

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
)

const appKeyPrefixLen = 8

// AppKeySecretLen 明文密钥长度（不含前缀分隔符）
const appKeySecretLen = 32

// NewAppKeyPlain 生成一个新的应用密钥明文，格式：olmp_<40 位随机串>
func NewAppKeyPlain() (string, error) {
	buf := make([]byte, appKeySecretLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	body := strings.TrimRight(base64.RawURLEncoding.EncodeToString(buf), "=")
	if len(body) > 40 {
		body = body[:40]
	}
	return "olmp_" + body, nil
}

// AppKeyPrefix 返回用于检索/展示的明文前缀
func AppKeyPrefix(plain string) string {
	if len(plain) <= appKeyPrefixLen {
		return plain
	}
	return plain[:appKeyPrefixLen]
}

func appKeyCipherKey() []byte {
	seed := conf.Conf.JwtSecret
	if seed == "" {
		seed = setting.GetStr(conf.Token)
	}
	sum := sha256.Sum256([]byte("openlist-media-appkey:" + seed))
	return sum[:]
}

func newGCM() (cipher.AEAD, error) {
	block, err := aes.NewCipher(appKeyCipherKey())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// EncryptAppKey 加密应用密钥明文
func EncryptAppKey(plain string) (string, error) {
	gcm, err := newGCM()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

// DecryptAppKey 解密应用密钥明文
func DecryptAppKey(secret string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM()
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("invalid app key secret")
	}
	nonce, data := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	plain, err := gcm.Open(nil, nonce, data, nil)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// HashToken 计算设备令牌的存储哈希
func HashToken(token string) string {
	sum := sha256.Sum256([]byte("openlist-emby-token:" + token))
	return hex.EncodeToString(sum[:])
}

// NewDeviceToken 生成 Emby 设备令牌
func NewDeviceToken() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// SubsonicToken 计算 Subsonic 的 t = md5(password + salt)
func SubsonicToken(password, salt string) string {
	sum := md5.Sum([]byte(password + salt))
	return hex.EncodeToString(sum[:])
}

// MatchSubsonicToken 校验客户端提交的 t 是否匹配
func MatchSubsonicToken(password, salt, token string) bool {
	return strings.EqualFold(SubsonicToken(password, salt), token)
}
