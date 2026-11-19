package intern_ai

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	// uaa-token: the value of cookie "uaa-token" on discovery.intern-ai.org.cn
	// (login via browser, F12 -> Application -> Cookies, or capture from a request)
	Token    string `json:"token" type:"string" required:"true" help:"uaa-token of discovery.intern-ai.org.cn (the value of the uaa-token cookie)"`
	APIBase  string `json:"api_base" type:"string" default:"https://discovery.intern-ai.org.cn" help:"InternAI (Shusheng) discovery platform base URL"`
	PageSize int    `json:"page_size" type:"number" default:"100" help:"entries per listing page (max 200)"`
}

var config = driver.Config{
	Name:        "InternAI",
	DefaultRoot: "/",
	// The download-url endpoint may fail transiently; retries are handled by resty.
	CheckStatus: true,
	// upload-url + PUT is supported
	NoUpload: false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &InternAI{}
	})
}
