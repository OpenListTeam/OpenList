package qbit

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
)

const (
	maxQbittorrentTorrentSize = 10 * 1024 * 1024
	torrentFetchTimeout       = 30 * time.Second
	maxTorrentRedirects       = 10
)

var nonPublicTorrentNetworks = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),  // Shared address space and common private overlays.
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments.
	netip.MustParsePrefix("192.0.2.0/24"),   // Documentation.
	netip.MustParsePrefix("192.88.99.0/24"), // Deprecated 6to4 relay anycast.
	netip.MustParsePrefix("198.18.0.0/15"),  // Benchmarking networks, often routed internally.
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"), // NAT64 can translate to private IPv4 destinations.
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001::/32"), // Teredo embeds IPv4 destinations.
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), // 6to4 embeds IPv4 destinations.
}

type torrentResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type torrentDialer interface {
	DialContext(context.Context, string, string) (net.Conn, error)
}

func isRemoteTorrentURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https"))
}

func isMagnetURL(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	return err == nil && strings.EqualFold(u.Scheme, "magnet")
}

func fetchTorrentDataFromURL(args *tool.AddUrlArgs) ([]byte, error) {
	return fetchTorrentData(args.Ctx, strings.TrimSpace(args.Url), newPublicTorrentHTTPClient())
}

func fetchTorrentData(ctx context.Context, rawURL string, client *http.Client) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid torrent URL: %w", err)
	}
	if err := validateRemoteTorrentURL(u); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create torrent request: %w", err)
	}
	req.Header.Set("User-Agent", base.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch torrent URL: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("failed to fetch torrent URL: unexpected HTTP status %s", resp.Status)
	}
	if resp.ContentLength > maxQbittorrentTorrentSize {
		return nil, fmt.Errorf("torrent data is too large, maximum size is 10MB")
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxQbittorrentTorrentSize+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read torrent URL: %w", err)
	}
	if len(data) > maxQbittorrentTorrentSize {
		return nil, fmt.Errorf("torrent data is too large, maximum size is 10MB")
	}
	if _, err := torrent.Decode(data); err != nil {
		return nil, fmt.Errorf("remote URL did not return a valid torrent: %w", err)
	}
	return data, nil
}

func newPublicTorrentHTTPClient() *http.Client {
	tlsInsecureSkipVerify := conf.Conf != nil && conf.Conf.TlsInsecureSkipVerify
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		// A proxy could resolve the hostname again and bypass the pinned public IP.
		Proxy:                 nil,
		DialContext:           publicTorrentDialContext(net.DefaultResolver, dialer),
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: tlsInsecureSkipVerify},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		IdleConnTimeout:       30 * time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   torrentFetchTimeout,
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxTorrentRedirects {
			return fmt.Errorf("stopped after %d redirects", maxTorrentRedirects)
		}
		return validateRemoteTorrentURL(req.URL)
	}
	return client
}

func publicTorrentDialContext(resolver torrentResolver, dialer torrentDialer) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid torrent host address %q: %w", address, err)
		}
		addrs, err := resolveTorrentHost(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		for _, addr := range addrs {
			if !isPublicTorrentAddress(addr) {
				return nil, fmt.Errorf("torrent URL resolves to a non-public address")
			}
		}

		var lastErr error
		for _, addr := range addrs {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, fmt.Errorf("failed to connect to torrent host: %w", lastErr)
	}
}

func resolveTorrentHost(ctx context.Context, resolver torrentResolver, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{addr}, nil
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve torrent host: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("torrent host resolved to no addresses")
	}
	return addrs, nil
}

func validateRemoteTorrentURL(u *url.URL) error {
	if u == nil || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return fmt.Errorf("torrent URL must use HTTP or HTTPS")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("torrent URL host is required")
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("torrent URL must not target localhost")
	}
	if addr, err := netip.ParseAddr(host); err == nil && !isPublicTorrentAddress(addr) {
		return fmt.Errorf("torrent URL must target a public address")
	}
	return nil
}

func isPublicTorrentAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return false
	}
	for _, prefix := range nonPublicTorrentNetworks {
		if prefix.Contains(addr) {
			return false
		}
	}
	return true
}
