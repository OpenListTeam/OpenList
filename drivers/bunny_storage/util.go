package bunny_storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	stdpath "path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

const (
	defaultEndpoint    = "storage.bunnycdn.com"
	defaultPlaceholder = ".openlist"

	cdnTokenMethodSHA256     = "sha256"
	cdnTokenMethodHMACSHA256 = "hmac_sha256"
)

func normalizeBaseURL(raw string, fallback string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = fallback
	}
	if raw == "" {
		return nil, fmt.Errorf("empty url")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("invalid url: %s", raw)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func cleanObjectPath(path string) string {
	if path == "" {
		return "/"
	}
	return stdpath.Clean("/" + strings.TrimPrefix(path, "/"))
}

func stripObjectPathPrefix(path string, prefix string) (string, bool) {
	path = cleanObjectPath(path)
	prefix = cleanObjectPath(prefix)
	if prefix == "/" {
		return path, false
	}
	if path == prefix {
		return "/", true
	}
	if strings.HasPrefix(path, prefix+"/") {
		return cleanObjectPath(strings.TrimPrefix(path, prefix)), true
	}
	return path, false
}

func isObjectPathOrChild(path string, parent string) bool {
	path = cleanObjectPath(path)
	parent = cleanObjectPath(parent)
	return path == parent || strings.HasPrefix(path, parent+"/")
}

func trimCDNBasePath(path string, mountPath string) string {
	path = cleanObjectPath(path)
	if path == "/" {
		return ""
	}
	if stripped, ok := stripObjectPathPrefix(path, mountPath); ok {
		path = stripped
	}
	if path == "/" {
		return ""
	}
	return strings.TrimRight(path, "/")
}

func (d *BunnyStorage) cdnObjectPath(path string) string {
	objectPath := cleanObjectPath(path)
	if stripped, ok := stripObjectPathPrefix(objectPath, d.GetStorage().MountPath); ok {
		objectPath = stripped
	}
	rootPath := cleanObjectPath(d.GetRootPath())
	if rootPath != "/" && !isObjectPathOrChild(objectPath, rootPath) {
		objectPath = cleanObjectPath(stdpath.Join(rootPath, objectPath))
	}
	return objectPath
}

func (d *BunnyStorage) placeholderName() string {
	if d.Placeholder == "" {
		return defaultPlaceholder
	}
	return d.Placeholder
}

func (d *BunnyStorage) storageURL(path string, dir bool) string {
	u := *d.endpoint
	cleanPath := cleanObjectPath(path)
	zone := strings.Trim(d.StorageZoneName, "/")
	if cleanPath == "/" {
		u.Path = "/" + zone + "/"
		return u.String()
	}
	u.Path = "/" + zone + "/" + strings.TrimPrefix(cleanPath, "/")
	if dir && !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	return u.String()
}

func (d *BunnyStorage) cdnURL(path string) string {
	u := *d.cdnBase
	cleanPath := cleanObjectPath(path)
	basePath := trimCDNBasePath(u.Path, d.GetStorage().MountPath)
	if cleanPath == "/" {
		if basePath == "" {
			u.Path = "/"
		} else {
			u.Path = basePath + "/"
		}
		return u.String()
	}
	u.Path = basePath + "/" + strings.TrimPrefix(cleanPath, "/")
	return u.String()
}

func (d *BunnyStorage) authRequest() *resty.Request {
	return d.client.R().SetHeader("AccessKey", d.AccessKey)
}

func (d *BunnyStorage) handleResponseError(resp *resty.Response) error {
	if resp == nil {
		return fmt.Errorf("empty response")
	}
	if resp.StatusCode() >= http.StatusOK && resp.StatusCode() < http.StatusMultipleChoices {
		return nil
	}
	message := strings.TrimSpace(resp.String())
	var apiErrors []apiError
	if err := json.Unmarshal(resp.Body(), &apiErrors); err == nil && len(apiErrors) > 0 && apiErrors[0].Message != "" {
		message = apiErrors[0].Message
	}
	switch resp.StatusCode() {
	case http.StatusUnauthorized, http.StatusForbidden:
		return errs.NewErr(errs.PermissionDenied, "bunny storage request failed: %s", message)
	case http.StatusNotFound:
		return errs.NewErr(errs.ObjectNotFound, "bunny storage request failed: %s", message)
	default:
		return fmt.Errorf("bunny storage request failed: %s: %s", resp.Status(), message)
	}
}

func (d *BunnyStorage) parseTimes(item bunnyObject) parsedTimes {
	return parsedTimes{
		modified: parseBunnyTime(item.LastChanged, d.Modified),
		created:  parseBunnyTime(item.DateCreated, time.Time{}),
	}
}

func parseBunnyTime(value string, fallback time.Time) time.Time {
	if value == "" {
		return fallback
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05.999999999", value); err == nil {
		return t
	}
	if t, err := time.Parse("2006-01-02T15:04:05", value); err == nil {
		return t
	}
	return fallback
}

func (d *BunnyStorage) toObj(parentPath string, item bunnyObject) model.Obj {
	times := d.parseTimes(item)
	return &model.Object{
		ID:       item.Guid,
		Path:     stdpath.Join(parentPath, item.ObjectName),
		Name:     item.ObjectName,
		Size:     item.Length,
		Modified: times.modified,
		Ctime:    times.created,
		IsFolder: item.IsDirectory,
	}
}

func canonicalQuery(values url.Values) (string, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key == "token" || key == "expires" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		vals := values[key]
		if len(vals) > 1 {
			return "", fmt.Errorf("duplicate query parameter %q is not supported", key)
		}
		value := ""
		if len(vals) == 1 {
			value = vals[0]
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "&"), nil
}

func (d *BunnyStorage) signCDNURL(rawURL string, clientIP string) (string, time.Duration, error) {
	return d.signCDNURLAt(rawURL, clientIP, time.Now())
}

func (d *BunnyStorage) signCDNURLAt(rawURL string, clientIP string, now time.Time) (string, time.Duration, error) {
	expire := time.Hour * time.Duration(d.SignURLExpire)
	if expire <= 0 {
		expire = 4 * time.Hour
	}
	expires := now.Add(expire).Unix()
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", 0, err
	}
	query := u.Query()
	parameterData, err := canonicalQuery(query)
	if err != nil {
		return "", 0, err
	}
	signaturePath, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		signaturePath = u.Path
	}
	if !d.CDNTokenIncludeIP {
		clientIP = ""
	}
	token := d.signCDNToken(signaturePath, strconv.FormatInt(expires, 10), parameterData, clientIP)
	query.Set("token", token)
	query.Set("expires", strconv.FormatInt(expires, 10))
	u.RawQuery = query.Encode()
	return u.String(), expire, nil
}

func (d *BunnyStorage) signCDNToken(signaturePath string, expires string, parameterData string, clientIP string) string {
	switch strings.ToLower(strings.TrimSpace(d.CDNTokenMethod)) {
	case cdnTokenMethodHMACSHA256:
		message := signaturePath + expires + parameterData + clientIP
		mac := hmac.New(sha256.New, []byte(d.CDNTokenKey))
		_, _ = mac.Write([]byte(message))
		return "HS256-" + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	default:
		hashableBase := d.CDNTokenKey + signaturePath + expires + parameterData + clientIP
		sum := sha256.Sum256([]byte(hashableBase))
		return base64.RawURLEncoding.EncodeToString(sum[:])
	}
}
