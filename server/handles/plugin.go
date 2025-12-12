package handles

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/plugin"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// InstallPluginReq 定义了安装插件 API 的请求体结构
type InstallPluginReq struct {
	// Source 是插件的来源地址，可以是：
	// 1. GitHub 仓库 URL (e.g., "https://github.com/user/repo")
	// 2. Zip 压缩包 URL (e.g., "https://example.com/plugin.zip")
	// 3. 本地 manifest 文件路径 (e.g., "/path/to/plugin.json")
	Source string `json:"source" binding:"required"`
}

// PluginIDReq 定义了需要插件 ID 的通用请求体结构
type PluginIDReq struct {
	ID string `json:"id" binding:"required"`
}

// --- API 处理器 ---

// ListPlugins godoc
// @Summary      List all installed plugins
// @Description  Get a list of all plugins that are currently installed.
// @Tags         plugin
// @Produce      json
// @Success      200 {object} common.Resp{data=[]model.Plugin} "A list of installed plugins"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/list [get]
func ListPlugins(c *gin.Context) {
	// 直接从数据库获取最新的插件列表，确保状态是最新的
	plugins, err := db.GetAllPlugins(c.Request.Context())
	if err != nil {
		log.Errorf("Failed to get all plugins from database: %v", err)
		common.ErrorResp(c, err, http.StatusInternalServerError)
		return
	}
	common.SuccessResp(c, plugins)
}

// InstallPlugin godoc
// @Summary      Install a new plugin
// @Description  Install a plugin from a source URL (GitHub, Zip) or a local path.
// @Tags         plugin
// @Accept       json
// @Produce      json
// @Param        req body InstallPluginReq true "Plugin source"
// @Success      200 {object} common.Resp{data=model.Plugin} "Plugin installed successfully"
// @Failure      400 {object} common.Resp "Bad request"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/install [post]
func InstallPlugin(c *gin.Context) {
	var req InstallPluginReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}

	log.Infof("Attempting to install plugin from source: %s", req.Source)

	pluginInfo, err := plugin.PluginManager.Install(c.Request.Context(), req.Source)
	if err != nil {
		log.Errorf("Failed to install plugin from source '%s': %v", req.Source, err)
		common.ErrorResp(c, err, http.StatusInternalServerError)
		return
	}

	log.Infof("Successfully installed plugin: %s (v%s)", pluginInfo.Name, pluginInfo.Version)
	common.SuccessResp(c, pluginInfo.Plugin)
}

// InstallPluginFromUpload godoc
// @Summary      Install a plugin from an uploaded zip file
// @Description  Upload a .zip file containing plugin.json and a .wasm file to install a new plugin.
// @Tags         plugin
// @Accept       multipart/form-data
// @Produce      json
// @Param        file formData file true "The plugin zip file to upload"
// @Success      200 {object} common.Resp{data=model.Plugin} "Plugin installed successfully"
// @Failure      400 {object} common.Resp "Bad request (e.g., no file uploaded)"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/upload [post]
func InstallPluginFromUpload(c *gin.Context) {
	// "file" 必须是前端上传文件时使用的表单字段名 (form field name)
	file, err := c.FormFile("file")
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("failed to get 'file' from form: %w", err), http.StatusBadRequest)
		return
	}

	log.Infof("Attempting to install plugin from uploaded file: %s", file.Filename)

	// 打开上传的文件以获取 io.Reader
	f, err := file.Open()
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("failed to open uploaded file: %w", err), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	// 调用管理器的 InstallFromUpload 方法
	pluginInfo, err := plugin.PluginManager.InstallFromUpload(c.Request.Context(), f, file.Filename)
	if err != nil {
		log.Errorf("Failed to install plugin from uploaded file '%s': %v", file.Filename, err)
		common.ErrorResp(c, err, http.StatusInternalServerError)
		return
	}

	log.Infof("Successfully installed plugin from upload: %s (v%s)", pluginInfo.Name, pluginInfo.Version)
	common.SuccessResp(c, pluginInfo.Plugin)
}

// UninstallPlugin godoc
// @Summary      Uninstall a plugin
// @Description  Uninstall a plugin by its ID.
// @Tags         plugin
// @Accept       json
// @Produce      json
// @Param        req body PluginIDReq true "Plugin ID to uninstall"
// @Success      200 {object} common.Resp "Plugin uninstalled successfully"
// @Failure      400 {object} common.Resp "Bad request"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/uninstall [post]
func UninstallPlugin(c *gin.Context) {
	var req PluginIDReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}

	log.Infof("Attempting to uninstall plugin with ID: %s", req.ID)

	if err := plugin.PluginManager.Uninstall(c.Request.Context(), req.ID); err != nil {
		log.Errorf("Failed to uninstall plugin '%s': %v", req.ID, err)
		common.ErrorResp(c, err, http.StatusInternalServerError)
		return
	}

	log.Infof("Successfully uninstalled plugin: %s", req.ID)
	common.SuccessResp(c, "Plugin uninstalled successfully")
}

// CheckForUpdates godoc
// @Summary      Check for plugin updates
// @Description  Checks all installed plugins from GitHub for available updates.
// @Tags         plugin
// @Produce      json
// @Success      200 {object} common.Resp{data=map[string]string} "A map of plugins with available updates (id: new_version)"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/updates/check [get]
func CheckForUpdates(c *gin.Context) {
	log.Info("Checking for plugin updates...")

	updates, err := plugin.PluginManager.CheckForUpdates(c.Request.Context())
	if err != nil {
		log.Errorf("Failed to check for plugin updates: %v", err)
		common.ErrorResp(c, err, http.StatusInternalServerError)
		return
	}

	log.Infof("Found %d available plugin updates.", len(updates))
	common.SuccessResp(c, updates)
}

// UpdatePlugin godoc
// @Summary      Update a plugin
// @Description  Update a specific plugin to its latest version. The plugin must have been installed from GitHub.
// @Tags         plugin
// @Accept       json
// @Produce      json
// @Param        req body PluginIDReq true "Plugin ID to update"
// @Success      200 {object} common.Resp{data=model.Plugin} "Plugin updated successfully"
// @Failure      400 {object} common.Resp "Bad request"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/update [post]
func UpdatePlugin(c *gin.Context) {
	var req PluginIDReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}

	log.Infof("Attempting to update plugin with ID: %s", req.ID)

	updatedPluginInfo, err := plugin.PluginManager.Update(c.Request.Context(), req.ID)
	if err != nil {
		log.Errorf("Failed to update plugin '%s': %v", req.ID, err)
		common.ErrorResp(c, err, http.StatusInternalServerError)
		return
	}

	log.Infof("Successfully updated plugin: %s", req.ID)
	common.SuccessResp(c, updatedPluginInfo.Plugin)
}

// internal/server/handles/plugin.go

// CheckForUpdateSingle godoc
// @Summary      Check for a single plugin update
// @Description  Checks a specific plugin for an available update.
// @Tags         plugin
// @Accept       json
// @Produce      json
// @Param        req body PluginIDReq true "Plugin ID to check"
// @Success      200 {object} common.Resp{data=map[string]string} "A map containing the new version if an update is available (e.g., {\"new_version\": \"1.1.0\"})"
// @Failure      400 {object} common.Resp "Bad request"
// @Failure      404 {object} common.Resp "Plugin not found or not eligible for update"
// @Failure      500 {object} common.Resp "Internal server error"
// @Router       /api/plugin/updates/check_one [post]
func CheckForUpdateSingle(c *gin.Context) {
	var req PluginIDReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, http.StatusBadRequest)
		return
	}

	log.Infof("Checking for update for plugin: %s", req.ID)

	newVersion, err := plugin.PluginManager.CheckForUpdate(c.Request.Context(), req.ID)
	if err != nil {
		// 区分是插件找不到还是检查过程出错
		if strings.Contains(err.Error(), "not found") {
			common.ErrorResp(c, err, http.StatusNotFound)
		} else {
			common.ErrorResp(c, err, http.StatusInternalServerError)
		}
		return
	}

	response := make(map[string]string)
	if newVersion != "" {
		response["new_version"] = newVersion
	}

	common.SuccessResp(c, response)
}
