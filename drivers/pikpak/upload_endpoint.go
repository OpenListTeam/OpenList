package pikpak

import (
	"net/url"
	"strings"
)

// The OSS SDK prefixes the bucket itself. PikPak can instead return an upload
// gateway or an already bucket-qualified endpoint, producing an unresolvable
// name such as vip-lixian-07.upload-a10b.mypikpak.com. The .net bucket hosts are
// also unresolvable; the same buckets are available under .mypikpak.com.
func normalizeUploadEndpoint(endpoint, bucket string) string {
	address := endpoint
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	u, err := url.Parse(address)
	if err != nil {
		return endpoint
	}
	// Do not reinterpret custom endpoints or URLs carrying extra components.
	if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Port() != "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return endpoint
	}

	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if bucket != "" {
		host = strings.TrimPrefix(host, strings.ToLower(bucket)+".")
	}
	if host != "mypikpak.com" && host != "mypikpak.net" {
		gateway, domain, _ := strings.Cut(host, ".")
		if !strings.HasPrefix(gateway, "upload-") || gateway == "upload-" ||
			(domain != "mypikpak.com" && domain != "mypikpak.net") {
			return endpoint
		}
	}
	return "https://mypikpak.com"
}
