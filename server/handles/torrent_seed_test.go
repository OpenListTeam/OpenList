package handles

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// withSeedSite pins the configured seed site for the duration of a test.
func withSeedSite(t *testing.T, site string) {
	t.Helper()
	previous := seedSiteURLProvider
	seedSiteURLProvider = func() string { return site }
	t.Cleanup(func() { seedSiteURLProvider = previous })
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", raw, err)
	}
	return u
}

// TestSameSeedHostNormalizesDefaultPort guards against the regression where a
// configured "https://pan.example.com" and an embedded
// "https://pan.example.com:443/..." were treated as different hosts, silently
// discarding otherwise valid sources.
func TestSameSeedHostNormalizesDefaultPort(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://pan.example.com", "https://pan.example.com:443/x", true},
		{"http://pan.example.com", "http://pan.example.com:80/x", true},
		{"https://pan.example.com:8443", "https://pan.example.com:8443/x", true},
		{"https://pan.example.com:8443", "https://pan.example.com", false},
		{"https://pan.example.com", "http://pan.example.com", false},
		{"https://pan.example.com", "https://evil.example.com", false},
		{"https://pan.example.com", "https://pan.example.com.evil.com", false},
	}
	for _, tc := range cases {
		if got := sameSeedHost(mustParse(t, tc.a), mustParse(t, tc.b)); got != tc.want {
			t.Errorf("sameSeedHost(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestValidateSeedHostRejectsForeignHosts is the core SSRF guarantee: a seed
// source may only ever point at the operator-configured site.
func TestValidateSeedHostRejectsForeignHosts(t *testing.T) {
	withSeedSite(t, "https://pan.example.com")

	rejected := []string{
		"http://169.254.169.254/latest/meta-data/", // cloud metadata
		"http://127.0.0.1:5244/api/fs/list",        // local admin API
		"http://localhost:5244/d/secret",
		"https://evil.example.com/d/secret",
		"https://pan.example.com.evil.com/d/x",
		"file:///etc/passwd",
		"ftp://pan.example.com/x",
		"https://user:pass@pan.example.com/d/x", // embedded credentials
	}
	for _, raw := range rejected {
		if err := validateSeedHost(mustParse(t, raw)); err == nil {
			t.Errorf("validateSeedHost(%q) accepted a disallowed URL", raw)
		}
	}

	allowed := []string{
		"https://pan.example.com/d/some/file",
		"https://pan.example.com:443/sd/abc123",
	}
	for _, raw := range allowed {
		if err := validateSeedHost(mustParse(t, raw)); err != nil {
			t.Errorf("validateSeedHost(%q) rejected a valid URL: %v", raw, err)
		}
	}
}

func TestValidateSeedHostWithoutConfiguredSite(t *testing.T) {
	withSeedSite(t, "")
	if err := validateSeedHost(mustParse(t, "https://pan.example.com/d/x")); err == nil {
		t.Fatal("validateSeedHost() accepted a source while seed_site_url is unset")
	}
}

// TestRedirectGuardBlocksSSRF is the regression test for the bypass: the first
// hop passes the host allow-list, but a redirect must not be allowed to escape
// to an internal address.
func TestRedirectGuardBlocksSSRF(t *testing.T) {
	withSeedSite(t, "https://pan.example.com")

	origin := mustParse(t, "https://pan.example.com/d/file")
	check := seedSourceHTTPClient.CheckRedirect

	// A redirect staying on the configured site is fine.
	sameHost := &http.Request{URL: mustParse(t, "https://pan.example.com/d/file-2")}
	if err := check(sameHost, []*http.Request{{URL: origin}}); err != nil {
		t.Fatalf("CheckRedirect() rejected a same-host redirect: %v", err)
	}

	// A redirect to the cloud metadata endpoint must be refused.
	metadata := &http.Request{URL: mustParse(t, "http://169.254.169.254/latest/meta-data/")}
	if err := check(metadata, []*http.Request{{URL: origin}}); err == nil {
		t.Fatal("CheckRedirect() allowed a redirect to the cloud metadata endpoint")
	}

	// A redirect to localhost must be refused.
	local := &http.Request{URL: mustParse(t, "http://127.0.0.1:5244/api/fs/list")}
	if err := check(local, []*http.Request{{URL: origin}}); err == nil {
		t.Fatal("CheckRedirect() allowed a redirect to localhost")
	}

	// A same-host redirect that downgrades https -> http must be refused.
	downgrade := &http.Request{URL: mustParse(t, "http://pan.example.com/d/file")}
	if err := check(downgrade, []*http.Request{{URL: origin}}); err == nil {
		t.Fatal("CheckRedirect() allowed a scheme downgrade")
	}
}

func TestRedirectGuardLimitsHopCount(t *testing.T) {
	withSeedSite(t, "https://pan.example.com")

	origin := mustParse(t, "https://pan.example.com/d/file")
	via := make([]*http.Request, maxSeedSourceRedirects)
	for i := range via {
		via[i] = &http.Request{URL: origin}
	}
	next := &http.Request{URL: mustParse(t, "https://pan.example.com/d/file-2")}
	if err := seedSourceHTTPClient.CheckRedirect(next, via); err == nil {
		t.Fatal("CheckRedirect() accepted more redirects than maxSeedSourceRedirects")
	}
}

// TestValidateSeedSourceEnforcesPathPrefix documents the per-type path contract.
func TestValidateSeedSourceEnforcesPathPrefix(t *testing.T) {
	withSeedSite(t, "https://pan.example.com")

	if err := validateSeedSource(torrent.SeedSource{
		Type: "openlist-direct",
		URL:  "https://pan.example.com/sd/abc",
	}); err == nil {
		t.Fatal("validateSeedSource() accepted a share path for a direct source")
	}
	if err := validateSeedSource(torrent.SeedSource{
		Type: "openlist-share",
		URL:  "https://pan.example.com/d/file",
	}); err == nil {
		t.Fatal("validateSeedSource() accepted a direct path for a share source")
	}
	if err := validateSeedSource(torrent.SeedSource{
		Type: "openlist-direct",
		URL:  "https://evil.example.com/d/file",
	}); err == nil {
		t.Fatal("validateSeedSource() accepted a foreign host")
	}
	if err := validateSeedSource(torrent.SeedSource{
		Type: "openlist-direct",
		URL:  "https://pan.example.com/d/file",
	}); err != nil {
		t.Fatalf("validateSeedSource() rejected a valid direct source: %v", err)
	}
}

// TestFirstUsableSeedSourceSkipsExpiredAndForeign verifies the selection logic
// only returns sources that are both on-site and not expired.
func TestFirstUsableSeedSourceSkipsExpiredAndForeign(t *testing.T) {
	withSeedSite(t, "https://pan.example.com")

	file := torrent.SeedFile{
		Sources: []torrent.SeedSource{
			{Type: "openlist-direct", URL: "https://evil.example.com/d/a"},
			{Type: "openlist-direct", URL: "https://pan.example.com/d/b", ExpiresAt: "2000-01-01T00:00:00Z"},
			{Type: "openlist-share", URL: "https://pan.example.com/sd/good"},
		},
	}
	if got := firstUsableSeedSource(file); got != "https://pan.example.com/sd/good" {
		t.Fatalf("firstUsableSeedSource() = %q, want the valid share source", got)
	}

	// No usable source at all.
	none := torrent.SeedFile{
		Sources: []torrent.SeedSource{
			{Type: "openlist-direct", URL: "https://evil.example.com/d/a"},
		},
	}
	if got := firstUsableSeedSource(none); got != "" {
		t.Fatalf("firstUsableSeedSource() = %q, want empty", got)
	}
}

// TestBuildSeedRapidUploadRequestRejectsMultiFile documents that a multi-file
// torrent cannot be described by a single rapid-upload request. Returning nil
// (instead of silently using Files[0] with the aggregate size) prevents sending
// the destination a size/hash combination that contradicts itself.
func TestBuildSeedRapidUploadRequestRejectsMultiFile(t *testing.T) {
	const md5Hex = "0123456789abcdef0123456789abcdef"

	multi := &torrent.Torrent{
		OpenList: &torrent.Seed{
			PieceSize: torrent.DefaultPieceSize,
			Files: []torrent.SeedFile{
				{Path: "a.bin", Size: 10, Hashes: torrent.SeedHashes{MD5: md5Hex}},
				{Path: "b.bin", Size: 20, Hashes: torrent.SeedHashes{MD5: md5Hex}},
			},
		},
	}
	if req := buildSeedRapidUploadRequest(multi, nil); req != nil {
		t.Fatalf("buildSeedRapidUploadRequest() accepted a multi-file torrent: %#v", req)
	}

	// A single file must still work.
	single := &torrent.Torrent{
		Info: torrent.TorrentInfo{Name: "a.bin", Length: 10},
		OpenList: &torrent.Seed{
			PieceSize: torrent.DefaultPieceSize,
			Files: []torrent.SeedFile{
				{Path: "a.bin", Size: 10, Hashes: torrent.SeedHashes{MD5: md5Hex}},
			},
		},
	}
	req := buildSeedRapidUploadRequest(single, nil)
	if req == nil {
		t.Fatal("buildSeedRapidUploadRequest() rejected a valid single-file torrent")
	}
	if req.Size != 10 {
		t.Fatalf("buildSeedRapidUploadRequest() size = %d, want 10", req.Size)
	}
	if got := req.Whole.GetHash(utils.MD5); !strings.EqualFold(got, md5Hex) {
		t.Fatalf("buildSeedRapidUploadRequest() MD5 = %q, want %q", got, md5Hex)
	}

	// A single file whose metadata size disagrees with the torrent length is
	// internally inconsistent and must also be refused.
	mismatched := &torrent.Torrent{
		Info: torrent.TorrentInfo{Name: "a.bin", Length: 99},
		OpenList: &torrent.Seed{
			PieceSize: torrent.DefaultPieceSize,
			Files: []torrent.SeedFile{
				{Path: "a.bin", Size: 10, Hashes: torrent.SeedHashes{MD5: md5Hex}},
			},
		},
	}
	if req := buildSeedRapidUploadRequest(mismatched, nil); req != nil {
		t.Fatalf("buildSeedRapidUploadRequest() accepted a size/hash mismatch: %#v", req)
	}
}

func TestBuildSeedRapidUploadRequestHandlesNil(t *testing.T) {
	if req := buildSeedRapidUploadRequest(nil, nil); req != nil {
		t.Fatalf("buildSeedRapidUploadRequest(nil) = %#v, want nil", req)
	}
}
