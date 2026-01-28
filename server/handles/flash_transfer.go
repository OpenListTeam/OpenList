package handles

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/flash_transfer"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/tache"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type FlashListReq struct {
	Key      string `json:"key"`
	ParentID string `json:"parent_id"`

	IsZip     bool   `json:"is_zip"`
	ZipFileID string `json:"zip_file_id"`
}

type FlashImportReq struct {
	DstPath    string                         `json:"dst_path" binding:"required"`
	Selections []flash_transfer.UserSelection `json:"selections" binding:"required"`
}

type FlashShowReq struct {
	PhysicalID string `json:"physical_id"`
}

func parseShareKey(input string) (string, string) {
	input = strings.TrimSpace(input)

	if _, err := uuid.Parse(input); err == nil {
		return input, ""
	}

	re := regexp.MustCompile(`qfile\.qq\.com/q/([a-zA-Z0-9]+)`)
	matches := re.FindStringSubmatch(input)

	if len(matches) > 1 {
		return "", matches[1]
	}
	return "", input
}

func FlashList(c *gin.Context) {
	var req FlashListReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	if req.Key == "" {
		common.ErrorStrResp(c, "key is required", 400)
		return
	}

	filesetID, shareCode := parseShareKey(req.Key)

	client := flash_transfer.NewFlashClient()
	fileName := ""

	if filesetID == "" && shareCode != "" {
		err, fid, filename := client.GetFileSetIdByCode(shareCode)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		filesetID = fid
		fileName = filename
	}

	if filesetID == "" {
		common.ErrorStrResp(c, "Invalid share code or fileset ID", 400)
		return
	}

	var result interface{}
	var err error

	if req.IsZip && req.ZipFileID != "" {
		result, err = client.GetCompressedFileFolder(filesetID, req.ZipFileID, req.ParentID)
	} else {
		result, err = client.GetFileFolder(filesetID, req.ParentID)
	}

	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, gin.H{
		"fileset_id":   filesetID,
		"fileset_name": fileName,
		"list":         result,
	})
}

func FlashImport(c *gin.Context) {
	var req FlashImportReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)

	dstPath, err := user.JoinPath(req.DstPath)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	// 鉴权  文件修改权限！！
	if !user.CanWrite() {
		meta, err := op.GetNearestMeta(path.Dir(dstPath))
		if err != nil && !errors.Is(err, errs.MetaNotFound) {
			common.ErrorResp(c, err, 500)
			return
		}

		if !common.CanWrite(meta, dstPath) {
			common.ErrorResp(c, errs.PermissionDenied, 403)
			return
		}
	}

	type LocalAddition struct {
		RootFolderPath string `json:"root_folder_path"`
	}

	storage, _, err := op.GetStorageAndActualPath(dstPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	sto := storage.GetStorage()
	if sto.Driver != "Local" {
		common.ErrorStrResp(c, "闪传导入暂仅支持导入到本地挂载目录(Local Storage)", 400)
		return
	}

	var addition LocalAddition
	err = json.Unmarshal([]byte(sto.Addition), &addition)
	if err != nil {
		common.ErrorStrResp(c, "解析本地存储配置失败: "+err.Error(), 500)
		return
	}
	realRootPath := addition.RootFolderPath

	if realRootPath == "" {
		common.ErrorStrResp(c, "无法获取本地存储的物理根路径", 500)
		return
	}

	client := flash_transfer.NewFlashClient()

	tasks, err := client.ResolveDownloads(req.Selections)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	baseTempDir := filepath.Join(os.TempDir(), "openlist_flash_transfer")

	client = flash_transfer.NewFlashClient()
	tasks, err = client.ResolveDownloads(req.Selections)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	var addedCount int
	for _, item := range tasks {
		relPathSlash := filepath.ToSlash(item.RelativePath)
		finalVirtualPath := path.Join(req.DstPath, relPathSlash)

		parentDir := path.Dir(finalVirtualPath)
		fileName := filepath.Base(item.RelativePath)

		taskTempDir := filepath.Join(baseTempDir, uuid.NewString())

		newTask := &tool.DownloadTask{
			TaskExtension: task.TaskExtension{
				Base: tache.Base{
					ID: uuid.NewString(),
				},
			},
			Url:           item.DownloadURL,
			DstDirPath:    parentDir,
			SuggestedName: fileName,
			TempDir:       taskTempDir,
			Toolname:      "SimpleHttp",
		}

		tool.DownloadTaskManager.Add(newTask)
		addedCount++
	}

	common.SuccessResp(c, gin.H{
		"message":     "Import started",
		"total_files": len(tasks),
		"added_tasks": addedCount,
	})
}

func FlashShow(c *gin.Context) {
	var req FlashShowReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	client := flash_transfer.NewFlashClient()
	physicalID := req.PhysicalID

	result, err := client.GetDownloadUrl(physicalID, physicalID)

	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	downloadUrl := result.Data.DownloadRsp[0].Url

	common.SuccessResp(c, gin.H{
		"download_url": downloadUrl,
	})
}
