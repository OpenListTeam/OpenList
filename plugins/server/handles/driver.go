package handles

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/common"
	"github.com/gin-gonic/gin"
)

func ListDriverInfo(c *gin.Context) {
	common.SuccessResp(c, op.GetDriverInfoMap())
}

func ListDriverNames(c *gin.Context) {
	common.SuccessResp(c, op.GetDriverNames())
}

func GetDriverInfo(c *gin.Context) {
	driverName := c.Query("driver")
	infoMap := op.GetDriverInfoMap()
	items, ok := infoMap[driverName]
	if !ok {
		common.ErrorStrResp(c, fmt.Sprintf("driver [%s] not found", driverName), 404)
		return
	}
	common.SuccessResp(c, items)
}
