package v4_1_9

import (
	"os"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func ResetSkipTlsVerify() {
	if !conf.Conf.TlsInsecureSkipVerify {
		return
	}
	if !strings.HasPrefix(conf.Version, "v") {
		return
	}

	conf.Conf.TlsInsecureSkipVerify = false

	confBody, err := utils.Json.MarshalIndent(conf.Conf, "", "  ")
	if err != nil {
		utils.Log.Errorf("[ResetSkipTlsVerify] failed to rewrite config: marshal config error: %+v", err)
		return
	}
	err = os.WriteFile(conf.ConfigPath, confBody, 0o777)
	if err != nil {
		utils.Log.Errorf("[ResetSkipTlsVerify] failed to rewrite config: update config struct error: %+v", err)
		return
	}
	utils.Log.Infof("[ResetSkipTlsVerify] succeeded to set tls_insecure_skip_verify to false")
}
