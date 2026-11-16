package data

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestInitialSettingsContainsPublicDownloadLinkBase(t *testing.T) {
	previousConf := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = previousConf })

	for _, item := range InitialSettings() {
		if item.Key != conf.DownloadLinkBase {
			continue
		}
		if item.Type != conf.TypeString {
			t.Fatalf("download_link_base type = %q, want %q", item.Type, conf.TypeString)
		}
		if item.Group != model.SITE {
			t.Fatalf("download_link_base group = %d, want SITE", item.Group)
		}
		if item.Flag != model.PUBLIC {
			t.Fatalf("download_link_base flag = %d, want PUBLIC", item.Flag)
		}
		if item.Value != "" {
			t.Fatalf("download_link_base default = %q, want empty", item.Value)
		}
		return
	}
	t.Fatal("download_link_base not found in InitialSettings()")
}
