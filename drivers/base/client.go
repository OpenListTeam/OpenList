package base

import (
	"crypto/tls"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/go-resty/resty/v2"
)

var (
	NoRedirectClient *resty.Client
	RestyClient      *resty.Client
	HttpClient       *http.Client
)
var UserAgent = "Mozilla/5.0 (Macintosh; Apple macOS 15_5) AppleWebKit/537.36 (KHTML, like Gecko) Safari/537.36 Chrome/138.0.0.0"
var DefaultTimeout = time.Second * 30

func InitClient() {
	NoRedirectClient = resty.New().SetRedirectPolicy(
		resty.RedirectPolicyFunc(func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		}),
	).SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
	NoRedirectClient.SetHeader("user-agent", UserAgent)
	net.SetRestyProxyIfConfigured(NoRedirectClient)

	RestyClient = NewRestyClient()
	HttpClient = net.NewHttpClient()
}

func NewRestyClient() *resty.Client {
	client := resty.New().
		SetHeader("user-agent", UserAgent).
		SetRetryCount(3).
		SetRetryResetReaders(true).
		SetTimeout(DefaultTimeout).
		SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})

	net.SetRestyProxyIfConfigured(client)
	return client
}
