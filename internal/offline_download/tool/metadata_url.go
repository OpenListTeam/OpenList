package tool

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

var (
	ErrCloudMetadataEndpoint = errors.New("access to cloud metadata endpoint is not allowed")
	lookupIPAddr             = net.DefaultResolver.LookupIPAddr
)

func ValidateOfflineDownloadURL(ctx context.Context, rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	u, err := url.Parse(rawURL)
	if err == nil && u.Host != "" {
		return validateOfflineDownloadHost(ctx, u.Hostname())
	}

	if err == nil && u.Scheme != "" {
		return nil
	}

	// Keep scheme-less URLs compatible: only reject direct metadata IP forms here,
	// instead of treating any leading path segment as a hostname and doing DNS.
	host := strings.Trim(rawURL, "[]")
	host, _, _ = strings.Cut(host, "/")
	host, _, _ = strings.Cut(host, "?")
	host, _, _ = strings.Cut(host, "#")
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = splitHost
	}
	if ip := net.ParseIP(host); isCloudMetadataIP(ip) {
		return ErrCloudMetadataEndpoint
	}
	return nil
}

func NewOfflineDownloadHTTPClient(base http.Client) *http.Client {
	client := base
	previousCheckRedirect := client.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := ValidateOfflineDownloadURL(req.Context(), req.URL.String()); err != nil {
			return err
		}
		if previousCheckRedirect != nil {
			return previousCheckRedirect(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &client
}

func validateOfflineDownloadHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(host); ip != nil {
		if isCloudMetadataIP(ip) {
			return ErrCloudMetadataEndpoint
		}
		return nil
	}

	addrs, err := lookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, addr := range addrs {
		if isCloudMetadataIP(addr.IP) {
			return ErrCloudMetadataEndpoint
		}
	}
	return nil
}

func isCloudMetadataIP(ip net.IP) bool {
	ip = ip.To4()
	return ip != nil && ip[0] == 169 && ip[1] == 254 && ip[2] == 169 && ip[3] == 254
}
