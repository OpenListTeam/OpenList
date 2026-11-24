package handles

import (
	"path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type SearchReq struct {
	model.SearchReq
	Password string `json:"password"`
}

type SearchResp struct {
	model.SearchNode
	Type int `json:"type"`
}

func Search(c *gin.Context) {
	var (
		req SearchReq
		err error
	)
	if err = c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	req.Parent, err = user.JoinPath(req.Parent)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := req.Validate(); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	nodes, total, err := search.SearchFiltered(c, req.SearchReq, func(node model.SearchNode) bool {
		return isSearchNodeAccessible(user, node, req.Password, op.GetNearestMeta)
	})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, common.PageResp{
		Content: utils.MustSliceConvert(nodes, nodeToSearchResp),
		Total:   total,
	})
}

func isSearchNodeAccessible(user *model.User, node model.SearchNode, password string, resolveMeta func(string) (*model.Meta, error)) bool {
	if !utils.IsSubPath(user.BasePath, node.Parent) {
		return false
	}
	nodePath := path.Join(node.Parent, node.Name)
	metaPath := node.Parent
	if node.IsDir {
		metaPath = nodePath
	}
	meta, err := resolveMeta(metaPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return false
	}
	return common.CanAccess(user, meta, nodePath, password)
}

func nodeToSearchResp(node model.SearchNode) SearchResp {
	return SearchResp{
		SearchNode: node,
		Type:       utils.GetObjType(node.Name, node.IsDir),
	}
}
