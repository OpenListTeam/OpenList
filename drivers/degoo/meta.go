package template

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	Username string `json:"username" required:"true" label:"Degoo Username" help:"Your Degoo account email"`
	Password string `json:"password" type:"password" required:"true" label:"Degoo Password" help:"Your Degoo account password"`
	// API key from the Python script. We can hardcode it here or make it an optional config item.
	// APIKey string `json:"apiKey" type:"password" label:"API Key" default:"da2-vs6twz5vnjdavpqndtbzg3prra"`
}

var config = driver.Config{
	Name: "Degoo",
	LocalSort: false,
	OnlyLinkMFile: false,
	OnlyProxy: false,
	NoCache: false,
	NoUpload: false,
	NeedMs: true,
	DefaultRoot: "0", // Root directory ID is "0" in the Python script.
	CheckStatus: true,
	Alert: "",
	NoOverwriteUpload: false,
	NoLinkURL: false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Degoo{}
	})
}
