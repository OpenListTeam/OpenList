package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
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
	if err := fs.BeginManualScan(req.Path, req.Limit); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c)
}

func StopManualScan(c *gin.Context) {
	if !fs.ManualScanRunning() {
		common.ErrorStrResp(c, "manual scan is not running", 400)
	}
	fs.StopManualScan()
	common.SuccessResp(c)
}

type ManualScanResp struct {
	ObjCount uint64 `json:"obj_count"`
	IsDone   bool   `json:"is_done"`
}

func GetManualScanProgress(c *gin.Context) {
	ret := ManualScanResp{
		ObjCount: fs.ScannedCount.Load(),
		IsDone:   !fs.ManualScanRunning(),
	}
	common.SuccessResp(c, ret)
}
