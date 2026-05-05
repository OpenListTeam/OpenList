package handles

import (
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func ListVirtualHosts(c *gin.Context) {
	var req model.PageReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	vhosts, total, err := op.GetVirtualHosts(req.Page, req.PerPage)
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, common.PageResp{
		Content: vhosts,
		Total:   total,
	})
}

func GetVirtualHost(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	vhost, err := op.GetVirtualHostById(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, vhost)
}

func CreateVirtualHost(c *gin.Context) {
	var req model.VirtualHost
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.CreateVirtualHost(&req); err != nil {
		common.ErrorResp(c, err, 500, true)
	} else {
		common.SuccessResp(c)
	}
}

func UpdateVirtualHost(c *gin.Context) {
	var req model.VirtualHost
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.UpdateVirtualHost(&req); err != nil {
		common.ErrorResp(c, err, 500, true)
	} else {
		common.SuccessResp(c)
	}
}

func DeleteVirtualHost(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.DeleteVirtualHostById(uint(id)); err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c)
}
