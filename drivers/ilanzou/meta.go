package template

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootID
	Username string `json:"username" type:"string" required:"true"`
	Password string `json:"password" type:"string" required:"true"`
	Ip       string `json:"ip" type:"string"`

	Token string
	UUID  string
}

type Conf struct {
	base       string
	secret     []byte
	bucket     string
	unproved   string
	proved     string
	devVersion string
	site       string
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &ILanZou{
			config: driver.Config{
				Name:              "ILanZou",
				DefaultRoot:       "0",
				LocalSort:         true,
				NoOverwriteUpload: true,
			},
			conf: Conf{
				base:       "https://api.ilanzou.com",
				secret:     []byte("lanZouY-disk-app"),
				bucket:     "wpanstore-lanzou",
				unproved:   "unproved",
				proved:     "proved",
				devVersion: "125",
				site:       "https://www.ilanzou.com",
			},
		}
	})
	op.RegisterDriver(func() driver.Driver {
		return &ILanZou{
			config: driver.Config{
				Name:              "FeijiPan",
				DefaultRoot:       "0",
				LocalSort:         true,
				NoOverwriteUpload: true,
			},
			conf: Conf{
				base:       "https://api.feijipan.com",
				secret:     []byte("dingHao-disk-app"),
				bucket:     "wpanstore",
				unproved:   "ws",
				proved:     "app",
				devVersion: "125",
				site:       "https://www.feijipan.com",
			},
		}
	})
}
