package modelscope

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// Addition holds the user-configurable fields of a ModelScope storage.
//
// A single ModelScope repository (a "repo file driver") maps to one OpenList
// storage mount. ModelScope stores files inside a git-style repository, so the
// natural mapping is:
//
//	ModelScope RepoID + RepoType + Revision  ->  one OpenList storage
//
// See drivers/github for a repository-oriented driver with the same shape.
type Addition struct {
	driver.RootPath

	Endpoint      string `json:"endpoint" type:"select" options:"https://modelscope.cn,https://modelscope.ai" default:"https://modelscope.cn"`
	Token         string `json:"token" type:"string" required:"true" help:"ModelScope access token from https://modelscope.cn/my/access/token"`
	RepoID        string `json:"repo_id" type:"string" required:"true" help:"owner/repository, e.g. van/my-files"`
	RepoType      string `json:"repo_type" type:"select" options:"model,dataset" default:"dataset"`
	Revision      string `json:"revision" type:"string" default:"master" help:"Branch, tag or commit SHA"`
	CommitMessage string `json:"commit_message" type:"string" default:"Upload from OpenList"`
}

var config = driver.Config{
	Name:              "ModelScope",
	LocalSort:         true,
	DefaultRoot:       "/",
	CheckStatus:       false,
	NoOverwriteUpload: false,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &ModelScope{}
	})
}
