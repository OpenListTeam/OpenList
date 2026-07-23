package bunny_storage

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestStorageURL(t *testing.T) {
	endpoint, err := normalizeBaseURL("ny.storage.bunnycdn.com", defaultEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	driver := &BunnyStorage{
		Addition: Addition{
			StorageZoneName: "my-zone",
		},
		endpoint: endpoint,
	}
	if got, want := driver.storageURL("/", true), "https://ny.storage.bunnycdn.com/my-zone/"; got != want {
		t.Fatalf("root list url = %q, want %q", got, want)
	}
	if got, want := driver.storageURL("/dir/a file.txt", false), "https://ny.storage.bunnycdn.com/my-zone/dir/a%20file.txt"; got != want {
		t.Fatalf("file url = %q, want %q", got, want)
	}
	if got, want := driver.storageURL("/dir", true), "https://ny.storage.bunnycdn.com/my-zone/dir/"; got != want {
		t.Fatalf("dir url = %q, want %q", got, want)
	}
}

func TestCDNURLWithBasePath(t *testing.T) {
	cdnBase, err := normalizeBaseURL("https://cdn.example.com/prefix/", "")
	if err != nil {
		t.Fatal(err)
	}
	driver := &BunnyStorage{cdnBase: cdnBase}
	if got, want := driver.cdnURL("/dir/a file.txt"), "https://cdn.example.com/prefix/dir/a%20file.txt"; got != want {
		t.Fatalf("cdn url = %q, want %q", got, want)
	}
}

func TestCDNURLUsesObjectPathWithoutMountPath(t *testing.T) {
	cdnBase, err := normalizeBaseURL("https://cdn.firmant.me", "")
	if err != nil {
		t.Fatal(err)
	}
	driver := &BunnyStorage{
		Storage: model.Storage{MountPath: "/BS"},
		cdnBase: cdnBase,
	}
	if got, want := driver.cdnURL(driver.cdnObjectPath("/BS/Video")), "https://cdn.firmant.me/Video"; got != want {
		t.Fatalf("cdn url = %q, want %q", got, want)
	}
}

func TestCDNURLDropsMountPathFromBaseURL(t *testing.T) {
	cdnBase, err := normalizeBaseURL("https://cdn.firmant.me/BS/", "")
	if err != nil {
		t.Fatal(err)
	}
	driver := &BunnyStorage{
		Storage: model.Storage{MountPath: "/BS"},
		cdnBase: cdnBase,
	}
	if got, want := driver.cdnURL(driver.cdnObjectPath("/Video")), "https://cdn.firmant.me/Video"; got != want {
		t.Fatalf("cdn url = %q, want %q", got, want)
	}
}

func TestCDNObjectPathKeepsRootFolderPath(t *testing.T) {
	driver := &BunnyStorage{}
	driver.MountPath = "/BS"
	driver.RootFolderPath = "/library"
	if got, want := driver.cdnObjectPath("/BS/Video"), "/library/Video"; got != want {
		t.Fatalf("cdn object path = %q, want %q", got, want)
	}
	if got, want := driver.cdnObjectPath("/library/Video"), "/library/Video"; got != want {
		t.Fatalf("cdn object path = %q, want %q", got, want)
	}
}

func TestLinkDisablesLongLivedCache(t *testing.T) {
	cdnBase, err := normalizeBaseURL("https://cdn.example.com", "")
	if err != nil {
		t.Fatal(err)
	}
	driver := &BunnyStorage{cdnBase: cdnBase}
	link, err := driver.Link(context.Background(), &model.Object{
		Path: "/video.mp4",
		Name: "video.mp4",
		Size: 123,
	}, model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	if link.Expiration == nil || *link.Expiration != 0 {
		t.Fatalf("link expiration = %v, want immediate cache expiration", link.Expiration)
	}
}

func TestSignCDNURL(t *testing.T) {
	driver := &BunnyStorage{
		Addition: Addition{
			CDNTokenKey:       "secret",
			CDNTokenIncludeIP: true,
			SignURLExpire:     1,
		},
	}
	signed, expire, err := driver.signCDNURLAt("https://zone.b-cdn.net/video.mp4?quality=high", "192.0.2.1", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if expire <= 0 {
		t.Fatal("expected positive expiration")
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	token := parsed.Query().Get("token")
	if want := "FxSpFem88zFo6uHFziwTuoMQTgDaD2PEn5n1zTMBUBI"; token != want {
		t.Fatalf("token = %q, want %q", token, want)
	}
	if parsed.Query().Get("expires") != "1700003600" {
		t.Fatalf("expires = %q, want 1700003600", parsed.Query().Get("expires"))
	}
	if parsed.Query().Get("quality") != "high" {
		t.Fatal("expected existing query parameters to be preserved")
	}
}

func TestSignCDNURLSupportsHMACSHA256(t *testing.T) {
	driver := &BunnyStorage{
		Addition: Addition{
			CDNTokenKey:       "secret",
			CDNTokenMethod:    cdnTokenMethodHMACSHA256,
			CDNTokenIncludeIP: true,
			SignURLExpire:     1,
		},
	}
	signed, _, err := driver.signCDNURLAt("https://zone.b-cdn.net/video.mp4?quality=high", "192.0.2.1", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Query().Get("token"), "HS256-sdrSSJE2JVwhSk2AoDUrmTV1muH6R5UHpZVcVfHeNxg"; got != want {
		t.Fatalf("token = %q, want %q", got, want)
	}
}

func TestSignCDNURLUsesDecodedPathForSHA256(t *testing.T) {
	driver := &BunnyStorage{
		Addition: Addition{
			CDNTokenKey:   "secret",
			SignURLExpire: 1,
		},
	}
	signed, _, err := driver.signCDNURLAt("https://zone.b-cdn.net/%E8%A7%86%E9%A2%91/%5Ba%20b%5D.mp4", "", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := parsed.Query().Get("token"), "yq1evD7klw0e3DjCbv8dJptbW4S4JwVW3GKLnxfeKGM"; got != want {
		t.Fatalf("token = %q, want %q", got, want)
	}
}

func TestSignCDNURLTreatsPlusAsLiteralPathCharacter(t *testing.T) {
	driver := &BunnyStorage{
		Addition: Addition{
			CDNTokenKey:   "secret",
			SignURLExpire: 1,
		},
	}
	now := time.Unix(1700000000, 0)
	literal, _, err := driver.signCDNURLAt("https://zone.b-cdn.net/a+b.mp4", "", now)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _, err := driver.signCDNURLAt("https://zone.b-cdn.net/a%2Bb.mp4", "", now)
	if err != nil {
		t.Fatal(err)
	}
	space, _, err := driver.signCDNURLAt("https://zone.b-cdn.net/a%20b.mp4", "", now)
	if err != nil {
		t.Fatal(err)
	}
	literalURL, err := url.Parse(literal)
	if err != nil {
		t.Fatal(err)
	}
	encodedURL, err := url.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	spaceURL, err := url.Parse(space)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := literalURL.Query().Get("token"), encodedURL.Query().Get("token"); got != want {
		t.Fatalf("literal plus token = %q, encoded plus token = %q", got, want)
	}
	if got, notWant := literalURL.Query().Get("token"), spaceURL.Query().Get("token"); got == notWant {
		t.Fatalf("literal plus token = %q, space token should differ", got)
	}
}

func TestSignCDNURLRejectsDuplicateQueryParameters(t *testing.T) {
	driver := &BunnyStorage{
		Addition: Addition{
			CDNTokenKey:   "secret",
			SignURLExpire: 1,
		},
	}
	_, _, err := driver.signCDNURLAt("https://zone.b-cdn.net/video.mp4?quality=high&quality=low", "", time.Unix(1700000000, 0))
	if err == nil {
		t.Fatal("expected duplicate query parameters to be rejected")
	}
	if got, want := err.Error(), `duplicate query parameter "quality" is not supported`; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestParseBunnyTimeSupportsFractionalSecondsWithoutTimezone(t *testing.T) {
	fallback := time.Unix(1, 0)
	got := parseBunnyTime("2023-03-21T13:38:31.693", fallback)
	want := time.Date(2023, 3, 21, 13, 38, 31, 693000000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("parsed time = %s, want %s", got.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

func TestConfigProxyMode(t *testing.T) {
	registeredConfig := (&BunnyStorage{}).Config()
	if registeredConfig.OnlyProxy {
		t.Fatal("driver registration config should allow users to choose proxy policy")
	}
	withoutCDN := (&BunnyStorage{Addition: Addition{StorageZoneName: "my-zone"}}).Config()
	if !withoutCDN.OnlyProxy {
		t.Fatal("storage API links require AccessKey headers and should be proxied without CDN")
	}
	withCDN := (&BunnyStorage{Addition: Addition{StorageZoneName: "my-zone", CDNBaseURL: "https://zone.b-cdn.net"}}).Config()
	if withCDN.OnlyProxy {
		t.Fatal("CDN links should be allowed to redirect directly")
	}
}
