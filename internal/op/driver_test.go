package op_test

import (
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers"
	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

func TestProductDriverCatalogMatchesBaseline(t *testing.T) {
	if err := op.InstallDrivers(drivers.All()); err != nil {
		t.Fatal(err)
	}
	const baseline = "115 Cloud|115 Open|115 Share|123 Open|123Pan|123PanLink|123PanShare|139Yun|" +
		"189Cloud|189CloudPC|189CloudTV|AList V3|AliDoc|Alias|Aliyundrive|AliyundriveOpen|AliyundriveShare|AutoIndex|" +
		"Azure Blob Storage|BaiduNetdisk|BaiduPhoto|Bunny Storage|CNB Releases|ChaoXingGroupDrive|Chunk|Cloudreve|Cloudreve V4|Crypt|Degoo|Doge|" +
		"Doubao|DoubaoNew|DoubaoShare|Dropbox|Emby|FTP|FebBox|FeijiPan|GitHub API|GitHub Releases|GoogleDrive|GooglePhoto|GuangYaPan|" +
		"HalalCloud|HalalCloudOpen|ILanZou|IPFS API|KodBox|Lanzou|LenovoNasShare|Local|MediaFire|MediaTrack|Mega_nz|Misskey|MoPan|NeteaseMusic|" +
		"Onedrive|Onedrive Sharelink|OnedriveAPP|OpenList|OpenListShare|PikPak|PikPakShare|ProtonDrive|Quark|QuarkOpen|QuarkTV|S3|SFTP|SMB|Seafile|Strm|" +
		"Teambition|Teldrive|Terabox|Thunder|ThunderBrowser|ThunderBrowserExpert|ThunderExpert|ThunderX|ThunderXExpert|UC|UCTV|USS|UrlTree|Virtual|WPS|WebDav|WeiYun|WoPan|YandexDisk|cloudflare_imgbed"
	want := strings.Split(baseline, "|")
	got := op.GetDriverNames()
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("driver names = %q, want %q", got, want)
	}
}

func TestInstallDriversRejectsInvalidCatalogAtomically(t *testing.T) {
	if err := op.InstallDrivers(drivers.All()); err != nil {
		t.Fatal(err)
	}
	want := op.GetDriverNames()
	sort.Strings(want)
	for _, tt := range []struct {
		name         string
		constructors []driver.Constructor
	}{
		{"nil constructor", []driver.Constructor{nil}},
		{"nil instance", []driver.Constructor{func() driver.Driver { return nil }}},
		{"typed nil instance", []driver.Constructor{func() driver.Driver { return (*local.Local)(nil) }}},
		{"duplicate product", []driver.Constructor{local.New, local.New}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := op.InstallDrivers(tt.constructors); err == nil {
				t.Fatal("invalid catalog was accepted")
			}
			got := op.GetDriverNames()
			sort.Strings(got)
			if !slices.Equal(got, want) {
				t.Fatalf("installed products changed after refusal: got %q", got)
			}
		})
	}
}
