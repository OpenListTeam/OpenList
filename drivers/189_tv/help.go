package _189_tv

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

func clientSuffix() map[string]string {
	return map[string]string{
		"clientType":        AndroidTV,
		"version":           TvVersion,
		"channelId":         TvChannelId,
		"clientSn":          "unknown",
		"model":             "PJX110",
		"osFamily":          "Android",
		"osVersion":         "35",
		"networkAccessMode": "WIFI",
		"telecomsOperator":  "46011",
	}
}

// SessionKeySignatureOfHmac HMAC签名
func SessionKeySignatureOfHmac(sessionSecret, sessionKey, operate, fullUrl, dateOfGmt string) string {
	urlpath := regexp.MustCompile(`://[^/]+((/[^/\s?#]+)*)`).FindStringSubmatch(fullUrl)[1]
	mac := hmac.New(sha1.New, []byte(sessionSecret))
	data := fmt.Sprintf("SessionKey=%s&Operate=%s&RequestURI=%s&Date=%s", sessionKey, operate, urlpath, dateOfGmt)
	mac.Write([]byte(data))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// AppKeySignatureOfHmac HMAC签名
func AppKeySignatureOfHmac(sessionSecret, appKey, operate, fullUrl string, timestamp int64) string {
	urlpath := regexp.MustCompile(`://[^/]+((/[^/\s?#]+)*)`).FindStringSubmatch(fullUrl)[1]
	mac := hmac.New(sha1.New, []byte(sessionSecret))
	data := fmt.Sprintf("AppKey=%s&Operate=%s&RequestURI=%s&Timestamp=%d", appKey, operate, urlpath, timestamp)
	mac.Write([]byte(data))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// 获取http规范的时间
func getHttpDateStr() string {
	return time.Now().UTC().Format(http.TimeFormat)
}

// 时间戳
func timestamp() int64 {
	return time.Now().UTC().UnixNano() / 1e6
}

type Time time.Time

func (t *Time) UnmarshalJSON(b []byte) error { return t.Unmarshal(b) }
func (t *Time) UnmarshalXML(e *xml.Decoder, ee xml.StartElement) error {
	b, err := e.Token()
	if err != nil {
		return err
	}
	if b, ok := b.(xml.CharData); ok {
		if err = t.Unmarshal(b); err != nil {
			return err
		}
	}
	return e.Skip()
}
func (t *Time) Unmarshal(b []byte) error {
	bs := strings.Trim(string(b), "\"")
	var v time.Time
	var err error
	for _, f := range []string{"2006-01-02 15:04:05 -07", "Jan 2, 2006 15:04:05 PM -07"} {
		v, err = time.ParseInLocation(f, bs+" +08", time.Local)
		if err == nil {
			break
		}
	}
	*t = Time(v)
	return err
}

type String string

func (t *String) UnmarshalJSON(b []byte) error { return t.Unmarshal(b) }
func (t *String) UnmarshalXML(e *xml.Decoder, ee xml.StartElement) error {
	b, err := e.Token()
	if err != nil {
		return err
	}
	if b, ok := b.(xml.CharData); ok {
		if err = t.Unmarshal(b); err != nil {
			return err
		}
	}
	return e.Skip()
}
func (s *String) Unmarshal(b []byte) error {
	*s = String(bytes.Trim(b, "\""))
	return nil
}

func toFamilyOrderBy(o string) string {
	switch o {
	case "filename":
		return "1"
	case "filesize":
		return "2"
	case "lastOpTime":
		return "3"
	default:
		return "1"
	}
}

func toDesc(o string) string {
	switch o {
	case "desc":
		return "true"
	case "asc":
		fallthrough
	default:
		return "false"
	}
}

func ParseHttpHeader(str string) map[string]string {
	header := make(map[string]string)
	for _, value := range strings.Split(str, "&") {
		if k, v, found := strings.Cut(value, "="); found {
			header[k] = v
		}
	}
	return header
}

func MustString(str string, err error) string {
	return str
}

func BoolToNumber(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isBool(bs ...bool) bool {
	for _, b := range bs {
		if b {
			return true
		}
	}
	return false
}

func IF[V any](o bool, t V, f V) V {
	if o {
		return t
	}
	return f
}
