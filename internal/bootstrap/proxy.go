package bootstrap

import (
	"net/url"
	"os"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	log "github.com/sirupsen/logrus"
)

// InitProxy 初始化代理设置
func InitProxy() {
	if conf.Conf.ProxyAddress != "" {
		// 验证代理地址格式
		if _, err := url.Parse(conf.Conf.ProxyAddress); err == nil {
			// 设置代理环境变量
			os.Setenv("HTTP_PROXY", conf.Conf.ProxyAddress)
			os.Setenv("HTTPS_PROXY", conf.Conf.ProxyAddress)
			os.Setenv("http_proxy", conf.Conf.ProxyAddress)
			os.Setenv("https_proxy", conf.Conf.ProxyAddress)
			log.Infof("proxy environment variables set: %s", conf.Conf.ProxyAddress)
		} else {
			log.Warnf("invalid proxy address format: %s, ignoring proxy setting", conf.Conf.ProxyAddress)
		}
	}
}
