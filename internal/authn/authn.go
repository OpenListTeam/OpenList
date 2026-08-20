package authn

import (
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

func NewAuthnInstance(c *gin.Context) (*webauthn.WebAuthn, error) {
	rawURL := common.GetApiUrl(c.Request.Context())
	siteURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	configuredURL, configuredErr := url.Parse(conf.Conf.SiteURL)
	configuredAbsolute := configuredErr == nil &&
		(configuredURL.Scheme == "http" || configuredURL.Scheme == "https") &&
		configuredURL.Hostname() != ""
	if !configuredAbsolute && !isLocalHostname(siteURL.Hostname()) {
		return nil, errors.New("passkeys require an absolute site_url in production")
	}
	return NewAuthnInstanceForURL(rawURL, setting.GetStr(conf.SiteTitle))
}

func NewAuthnInstanceForURL(rawURL, displayName string) (*webauthn.WebAuthn, error) {
	siteURL, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if siteURL.Hostname() == "" || (siteURL.Scheme != "http" && siteURL.Scheme != "https") {
		return nil, errors.New("passkeys require an absolute http(s) site_url")
	}
	if siteURL.Scheme != "https" && !isLocalHostname(siteURL.Hostname()) {
		return nil, errors.New("passkeys require https outside localhost")
	}
	origin := siteURL.Scheme + "://" + siteURL.Host
	return webauthn.New(&webauthn.Config{
		RPDisplayName: displayName,
		RPID:          siteURL.Hostname(),
		RPOrigins:     []string{origin},
		RPTopOrigins:  []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			UserVerification: protocol.VerificationRequired,
		},
		RPTopOriginVerificationMode: protocol.TopOriginExplicitVerificationMode,
		Timeouts: webauthn.TimeoutsConfig{
			Login: webauthn.TimeoutConfig{
				Enforce: true,
				Timeout: ChallengeTTL,
			},
			Registration: webauthn.TimeoutConfig{
				Enforce: true,
				Timeout: ChallengeTTL,
			},
		},
	})
}

func isLocalHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}
