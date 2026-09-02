package huggingface

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	ApiToken string `json:"api_token" type:"string" help:"HuggingFace API token, optional for public repos"`
	RepoID   string `json:"repo_id" type:"string" required:"true" help:"Repository ID, e.g. username/repo_name"`
	Ref      string `json:"ref" type:"string" help:"Branch, tag or commit SHA, main branch by default"`
	RepoType string `json:"repo_type" type:"select" options:"model,dataset,space" default:"model" help:"Repository type"`
	HFProxy  string `json:"hf_proxy" type:"string" help:"HuggingFace proxy, e.g. https://hf-mirror.com"`
}

var config = driver.Config{
	Name:        "HuggingFace",
	LocalSort:   true,
	DefaultRoot: "/",
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &HuggingFace{}
	})
}
