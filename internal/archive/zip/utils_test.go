package zip

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/archive/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/stretchr/testify/require"
)

func TestZipAcceptsLivpExtension(t *testing.T) {
	_, ok := tool.Tools[".livp"]
	require.True(t, ok)
}

func setNonEFSZipEncoding(value string) {
	op.Cache.SetSetting(conf.NonEFSZipEncoding, &model.SettingItem{
		Key:   conf.NonEFSZipEncoding,
		Value: value,
	})
}

func TestDecodeNamePrefersValidUTF8WhenEFSDisabled(t *testing.T) {
	setNonEFSZipEncoding("IBM437")

	name := "中文.txt"
	require.Equal(t, name, decodeName(name, false))
	// Ensure the setting still exists to verify we did not bypass config due to missing setup.
	require.Equal(t, "IBM437", setting.GetStr(conf.NonEFSZipEncoding))
}

func TestDecodeNameFallsBackToConfiguredEncoding(t *testing.T) {
	setNonEFSZipEncoding("GB18030")

	name := string([]byte{0xd6, 0xd0, 0xce, 0xc4, '.', 't', 'x', 't'})
	require.Equal(t, "中文.txt", decodeName(name, false))
}

func TestDecodeNameRespectsEFSFlag(t *testing.T) {
	setNonEFSZipEncoding("GB18030")

	name := "utf8-name.txt"
	require.Equal(t, name, decodeName(name, true))
}
