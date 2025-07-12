package handles

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func ListSyncerTask(c *gin.Context) {
	var req model.PageReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	log.Debugf("%+v", req)
	syncerTasks, total, err := op.GetTaskArgs(req.Page, req.PerPage)
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, common.PageResp{
		Content: syncerTasks,
		Total:   total,
	})
}

func CreateSyncerTask(c *gin.Context) {
	var req model.SyncerTaskArgs
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.CreateSyncerTaskArgs(&req); err != nil {
		common.ErrorResp(c, err, 500, true)
	} else {
		common.SuccessResp(c)
	}
}

func UpdateSyncerTask(c *gin.Context) {
	var req model.SyncerTaskArgs
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, err := op.GetTaskArgsById(req.ID)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if err := op.UpdateSyncerTaskArgs(&req); err != nil {
		common.ErrorResp(c, err, 500)
	} else {
		common.SuccessResp(c)
	}
}

func DeleteSyncerTask(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if err := op.DeleteSyncerTaskArgsById(uint(id)); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

func GetSyncerTask(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	syncerTask, err := op.GetTaskArgsById(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	common.SuccessResp(c, syncerTask)
}

func RunSyncer(c *gin.Context) {
	idStr := c.Query("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	syncerTask, err := op.GetTaskArgsById(uint(id))
	if err != nil {
		common.ErrorStrResp(c, "syncer task not find", 500)
		return
	}

	var addedTasks []task.TaskExtensionInfo

	t, err := fs.Syncer(c, *syncerTask)
	if t != nil {
		addedTasks = append(addedTasks, t)
	}
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	if len(addedTasks) > 0 {
		common.SuccessResp(c, gin.H{
			"message": fmt.Sprintf("Successfully created %d sync task(s)", len(addedTasks)),
			"tasks":   getTaskInfos(addedTasks),
		})
	} else {
		common.SuccessResp(c, gin.H{
			"message": "No sync tasks were added",
		})
	}
}

func CancelSyncer(c *gin.Context) {
	id := c.Query("id")
	if len(id) == 0 {
		common.ErrorStrResp(c, "id is required", 400)
	}
	fs.SyncTaskManager.Cancel(id)
	common.SuccessResp(c)
}

func GetTaskInfo(c *gin.Context) {
	id := c.Query("id")
	if len(id) == 0 {
		allTask := fs.SyncTaskManager.GetAll()
		common.SuccessResp(c, allTask)
	} else {
		syncTaskInfo, _ := fs.SyncTaskManager.GetByID(id)
		common.SuccessResp(c, syncTaskInfo)
	}
}
