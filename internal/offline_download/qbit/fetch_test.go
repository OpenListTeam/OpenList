package qbit

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/pkg/qbittorrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
)

type recordingClient struct {
	qbittorrent.Client
	linkCalls    int
	torrentCalls int
	link         string
	torrentData  []byte
}

func (c *recordingClient) AddFromLink(link, _, _ string) error {
	c.linkCalls++
	c.link = link
	return nil
}

func (c *recordingClient) AddFromTorrent(data []byte, _, _ string) error {
	c.torrentCalls++
	c.torrentData = append([]byte(nil), data...)
	return nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staticResolver struct {
	addrs []netip.Addr
}

func (r staticResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addrs, nil
}

type recordingDialer struct {
	addresses []string
}

func (d *recordingDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	return nil, errors.New("test dial stopped")
}

func TestIsPublicTorrentAddress(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "8.8.8.8", want: true},
		{address: "2606:4700:4700::1111", want: true},
		{address: "0.0.0.0", want: false},
		{address: "127.0.0.1", want: false},
		{address: "10.0.0.1", want: false},
		{address: "172.16.0.1", want: false},
		{address: "192.168.0.1", want: false},
		{address: "169.254.169.254", want: false},
		{address: "100.100.100.200", want: false},
		{address: "198.18.0.1", want: false},
		{address: "::1", want: false},
		{address: "fc00::1", want: false},
		{address: "fe80::1", want: false},
		{address: "::ffff:127.0.0.1", want: false},
		{address: "64:ff9b::7f00:1", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			if got := isPublicTorrentAddress(netip.MustParseAddr(tt.address)); got != tt.want {
				t.Fatalf("isPublicTorrentAddress(%s) = %v, want %v", tt.address, got, tt.want)
			}
		})
	}
}

func TestQBittorrentCapabilities(t *testing.T) {
	capabilities := (&QBittorrent{}).Capabilities()
	if !capabilities.TorrentData {
		t.Fatal("qBittorrent did not advertise torrent data support")
	}
}

func TestQBittorrentRejectsPrivateTorrentURLWithoutFallback(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	client := &recordingClient{}
	qbitTool := New(client)
	_, err := qbitTool.AddURL(&tool.AddUrlArgs{
		Ctx: context.Background(),
		Url: server.URL + "/file.torrent",
		UID: "task-id",
	})
	if err == nil || !strings.Contains(err.Error(), "public address") {
		t.Fatalf("AddURL() error = %v, want public-address rejection", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("private server received %d requests", requests.Load())
	}
	if client.linkCalls != 0 || client.torrentCalls != 0 {
		t.Fatalf("qBittorrent calls = links:%d torrents:%d, want none", client.linkCalls, client.torrentCalls)
	}
}

func TestPublicTorrentDialRejectsPrivateDNSResult(t *testing.T) {
	dialer := &recordingDialer{}
	dial := publicTorrentDialContext(staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("93.184.216.34"),
		netip.MustParseAddr("127.0.0.1"),
	}}, dialer)

	if _, err := dial(context.Background(), "tcp", "example.com:80"); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("dial error = %v, want non-public rejection", err)
	}
	if len(dialer.addresses) != 0 {
		t.Fatalf("dialer called with %v before all DNS results were validated", dialer.addresses)
	}
}

func TestPublicTorrentDialPinsValidatedIPAddress(t *testing.T) {
	dialer := &recordingDialer{}
	dial := publicTorrentDialContext(staticResolver{addrs: []netip.Addr{
		netip.MustParseAddr("93.184.216.34"),
	}}, dialer)

	_, err := dial(context.Background(), "tcp", "example.com:443")
	if err == nil || !strings.Contains(err.Error(), "test dial stopped") {
		t.Fatalf("dial error = %v", err)
	}
	if len(dialer.addresses) != 1 || dialer.addresses[0] != "93.184.216.34:443" {
		t.Fatalf("dial addresses = %v, want pinned IP", dialer.addresses)
	}
}

func TestQBittorrentKeepsMagnetLinkFlow(t *testing.T) {
	client := &recordingClient{}
	qbitTool := New(client)
	const magnet = "magnet:?xt=urn:btih:test"

	id, err := qbitTool.AddURL(&tool.AddUrlArgs{Ctx: context.Background(), Url: magnet, UID: "task-id"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "task-id" || client.linkCalls != 1 || client.link != magnet || client.torrentCalls != 0 {
		t.Fatalf("unexpected magnet result: id=%q links=%d link=%q torrents=%d", id, client.linkCalls, client.link, client.torrentCalls)
	}
}

func TestQBittorrentRejectsUnsupportedURLScheme(t *testing.T) {
	client := &recordingClient{}
	qbitTool := New(client)

	_, err := qbitTool.AddURL(&tool.AddUrlArgs{Ctx: context.Background(), Url: "ftp://127.0.0.1/file.torrent", UID: "task-id"})
	if err == nil || !strings.Contains(err.Error(), "only supports") {
		t.Fatalf("AddURL() error = %v, want unsupported-scheme rejection", err)
	}
	if client.linkCalls != 0 || client.torrentCalls != 0 {
		t.Fatalf("qBittorrent calls = links:%d torrents:%d, want none", client.linkCalls, client.torrentCalls)
	}
}

func TestFetchTorrentDataAcceptsValidPublicResponse(t *testing.T) {
	torrentData, err := torrent.NewTorrent("test.bin", 1, "").Encode()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "example.com" {
			t.Fatalf("request host = %q", req.URL.Host)
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(strings.NewReader(string(torrentData))),
			ContentLength: int64(len(torrentData)),
			Header:        make(http.Header),
			Request:       req,
		}, nil
	})}

	got, err := fetchTorrentData(context.Background(), "https://example.com/file.torrent", client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(torrentData) {
		t.Fatal("fetched torrent data changed")
	}
}

func TestTorrentRedirectRejectsLocalhost(t *testing.T) {
	client := newPublicTorrentHTTPClient()
	req, err := http.NewRequest(http.MethodGet, "http://localhost/internal", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.CheckRedirect(req, nil); err == nil {
		t.Fatal("localhost redirect was accepted")
	}
}
