package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

type ManualScanReq struct {
	Path  string  `json:"path"`
	Limit float64 `json:"limit"`
}

func StartManualScan(c *gin.Context) {
	var req ManualScanReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.BeginManualScan(req.Path, req.Limit); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c)
}

func StopManualScan(c *gin.Context) {
	if !op.ManualScanRunning() {
		common.ErrorStrResp(c, "manual scan is not running", 400)
		return
	}
	op.StopManualScan()
	common.SuccessResp(c)
}

type ManualScanResp struct {
	ObjCount uint64 `json:"obj_count"`
	IsDone   bool   `json:"is_done"`
}

func GetManualScanProgress(c *gin.Context) {
	ret := ManualScanResp{
		ObjCount: op.ScannedCount.Load(),
		IsDone:   !op.ManualScanRunning(),
	}
	common.SuccessResp(c, ret)
}
