package bootstrap

import (
	"os"
	"regexp"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	log "github.com/sirupsen/logrus"
)

// InitProxy 初始化代理设置
func InitProxy() {
	if conf.Conf.ProxyAddress != "" {
		// 验证代理地址格式
		if !isValidProxyAddress(conf.Conf.ProxyAddress) {
			log.Warnf("invalid proxy address format: %s, ignoring proxy setting", conf.Conf.ProxyAddress)
			return
		}

		// 设置代理环境变量
		os.Setenv("HTTP_PROXY", conf.Conf.ProxyAddress)
		os.Setenv("HTTPS_PROXY", conf.Conf.ProxyAddress)
		os.Setenv("http_proxy", conf.Conf.ProxyAddress)
		os.Setenv("https_proxy", conf.Conf.ProxyAddress)
		log.Infof("proxy environment variables set: %s", conf.Conf.ProxyAddress)
	}
}

// isValidProxyAddress 验证代理地址格式
func isValidProxyAddress(address string) bool {
	// 使用正则表达式验证代理地址格式
	// 支持 HTTP/HTTPS 代理：http://host:port 或 https://host:port
	// 支持 SOCKS 代理：socks5://host:port 或 socks4://host:port
	matched, _ := regexp.MatchString(`^(https?|socks[45])://[a-zA-Z0-9.-]+:\d+$`, address)
	return matched
}
