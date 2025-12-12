package plugin

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/coreos/go-semver/semver"
	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

var (
	PluginManager *Manager
)

// PluginInfo 只包含从数据库加载的插件元数据。
type PluginInfo struct {
	*model.Plugin
	handler PluginHandler // 缓存与此插件匹配的处理器
	driver  *DriverPlugin // 缓存已创建的驱动插件实例
}

// PluginHandler 定义了处理特定类型插件的接口
type PluginHandler interface {
	// Prefix 返回此处理器能处理的插件ID前缀
	Prefix() string
	// Register 注册一个插件
	Register(ctx context.Context, plugin *PluginInfo) error
	// Unregister 注销一个插件
	Unregister(ctx context.Context, plugin *PluginInfo) error
}

// Manager 负责管理插件的生命周期（安装、卸载、加载元数据）。
type Manager struct {
	sync.RWMutex
	plugins    map[string]*PluginInfo // Key: 插件 ID
	pluginDir  string
	httpClient *http.Client
	handlers   []PluginHandler // 插件处理器列表
}

// NewManager 创建一个新的、轻量级的插件管理器。
func NewManager(ctx context.Context, dataDir string) (*Manager, error) {
	pluginDir := filepath.Join(dataDir, "plugins")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create plugin directory: %w", err)
	}

	m := &Manager{
		plugins:    make(map[string]*PluginInfo),
		pluginDir:  pluginDir,
		httpClient: &http.Client{},
		// 在这里注册所有支持的插件处理器
		handlers: []PluginHandler{
			&DriverPluginHandler{}, // 注册驱动插件处理器
			// 未来可以添加 newThemePluginHandler(), newOtherPluginHandler() 等
		},
	}

	if err := m.loadPluginsFromDB(ctx); err != nil {
		return nil, fmt.Errorf("failed to load plugins from database: %w", err)
	}

	// 在 NewManager 中直接调用 RegisterAll，确保启动时所有插件都被注册
	m.RegisterAll(ctx)

	return m, nil
}

// loadPluginsFromDB 在启动时仅从数据库加载插件元数据。
func (m *Manager) loadPluginsFromDB(ctx context.Context) error {
	storedPlugins, err := db.GetAllPlugins(ctx)
	if err != nil {
		return err
	}
	log.Infof("Found %d installed plugins in the database.", len(storedPlugins))
	for _, p := range storedPlugins {
		if _, err := os.Stat(p.WasmPath); os.IsNotExist(err) {
			log.Warnf("Plugin '%s' found in database but its wasm file is missing at %s. Skipping.", p.ID, p.WasmPath)
			continue
		}
		pluginInfo := &PluginInfo{Plugin: p}
		// 为插件找到匹配的处理器
		for _, h := range m.handlers {
			if strings.HasPrefix(p.ID, h.Prefix()) {
				pluginInfo.handler = h
				break
			}
		}
		if pluginInfo.handler == nil {
			log.Warnf("No handler found for plugin type with ID '%s'. Skipping registration.", p.ID)
		}
		m.plugins[p.ID] = pluginInfo
		log.Infof("Loaded plugin metadata: %s (v%s)", p.Name, p.Version)
	}
	return nil
}

// RegisterAll 遍历所有已加载的插件，并使用对应的处理器进行注册。
func (m *Manager) RegisterAll(ctx context.Context) {
	m.RLock()
	defer m.RUnlock()
	log.Infof("Registering all loaded plugins...")
	for id, pluginInfo := range m.plugins {
		if pluginInfo.handler != nil {
			if err := pluginInfo.handler.Register(ctx, pluginInfo); err != nil {
				// 注册失败，更新数据库状态
				log.Errorf("Failed to register plugin '%s': %v", id, err)
				pluginInfo.Status = model.StatusError
				pluginInfo.Message = err.Error()
				// 更新数据库
				if err := db.UpdatePluginStatus(ctx, id, model.StatusError, err.Error()); err != nil {
					log.Errorf("Failed to update status for plugin '%s' in database: %v", id, err)
				}
			} else {
				// 注册成功，更新状态
				pluginInfo.Status = model.StatusActive
				pluginInfo.Message = ""
				if err := db.UpdatePluginStatus(ctx, id, model.StatusActive, ""); err != nil {
					log.Errorf("Failed to update status for plugin '%s' in database: %v", id, err)
				}
			}
		}
	}
}

// Install 根据源字符串的格式自动选择安装方式。
func (m *Manager) Install(ctx context.Context, source string) (*PluginInfo, error) {
	if strings.HasSuffix(source, ".zip") {
		log.Infof("Installing plugin from archive URL: %s", source)
		return m.InstallFromArchiveURL(ctx, source)
	}
	if strings.HasPrefix(source, "https://github.com/") {
		log.Infof("Installing plugin from GitHub repository: %s", source)
		return m.InstallFromGitHub(ctx, source)
	}
	// 默认认为是本地文件系统路径
	log.Infof("Installing plugin from local path: %s", source)
	return m.InstallFromLocal(ctx, source, "")
}

// InstallFromLocal 从本地清单和 Wasm 文件安装插件。
// manifestPath 是必需的，wasmPath 是可选的（如果为空，则在 manifestPath 相同目录下查找 .wasm 文件）。
func (m *Manager) InstallFromLocal(ctx context.Context, manifestPath string, wasmPath string) (*PluginInfo, error) {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest file '%s': %w", manifestPath, err)
	}

	if wasmPath == "" {
		wasmPath = strings.TrimSuffix(manifestPath, filepath.Ext(manifestPath)) + ".wasm"
	}

	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read wasm file at '%s': %w", wasmPath, err)
	}

	return m.install(ctx, manifestBytes, wasmBytes, "local:"+manifestPath)
}

// InstallFromUpload 从一个上传的文件流 (io.Reader) 安装插件。
func (m *Manager) InstallFromUpload(ctx context.Context, fileReader io.Reader, originalFileName string) (*PluginInfo, error) {
	// 1. 将上传的文件内容保存到一个临时文件中
	tmpFile, err := os.CreateTemp("", "plugin-upload-*.zip")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary file for upload: %w", err)
	}
	defer os.Remove(tmpFile.Name())

	_, err = io.Copy(tmpFile, fileReader)
	if err != nil {
		return nil, fmt.Errorf("failed to save uploaded file to temporary location: %w", err)
	}
	// 必须关闭文件，以便 zip.OpenReader 能够读取它
	tmpFile.Close()

	// 2. 从这个临时的 zip 文件中提取 manifest 和 wasm
	manifestBytes, wasmBytes, err := extractPluginFromZip(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to extract plugin from uploaded archive: %w", err)
	}

	// 3. 调用核心安装逻辑，使用 "upload:[filename]" 作为来源标识
	return m.install(ctx, manifestBytes, wasmBytes, "upload:"+originalFileName)
}

// InstallFromArchiveURL 从一个 zip 压缩包的 URL 安装插件。
func (m *Manager) InstallFromArchiveURL(ctx context.Context, url string) (*PluginInfo, error) {
	tmpFile, err := downloadTempFile(m.httpClient, url)
	if err != nil {
		return nil, fmt.Errorf("failed to download archive from %s: %w", url, err)
	}
	defer os.Remove(tmpFile.Name())

	manifestBytes, wasmBytes, err := extractPluginFromZip(tmpFile.Name())
	if err != nil {
		return nil, fmt.Errorf("failed to extract plugin from archive '%s': %w", url, err)
	}

	return m.install(ctx, manifestBytes, wasmBytes, url)
}

// InstallFromGitHub 从 GitHub 仓库的最新 release 安装插件。
func (m *Manager) InstallFromGitHub(ctx context.Context, repoURL string) (*PluginInfo, error) {
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return nil, fmt.Errorf("invalid github repo URL format: %s", repoURL)
	}
	owner, repo := parts[0], parts[1]

	// 1. 获取最新 release 信息
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)
	log.Infof("Fetching latest release from GitHub API: %s", apiURL)

	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned non-200 status: %s", resp.Status)
	}

	var release struct {
		Assets []struct {
			Name        string `json:"name"`
			DownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, fmt.Errorf("failed to parse GitHub API response: %w", err)
	}

	// 2. 查找包含插件的 zip 资产
	var assetURL string
	for _, asset := range release.Assets {
		// 寻找第一个 .zip 文件作为目标
		if strings.HasSuffix(asset.Name, ".zip") {
			assetURL = asset.DownloadURL
			break
		}
	}

	if assetURL == "" {
		return nil, fmt.Errorf("no .zip asset found in the latest release of %s/%s", owner, repo)
	}

	log.Infof("Found release asset to download: %s", assetURL)
	return m.InstallFromArchiveURL(ctx, assetURL)
}

// install 是安装插件的核心逻辑
func (m *Manager) install(ctx context.Context, manifestBytes []byte, wasmBytes []byte, sourceURL string) (*PluginInfo, error) {
	m.Lock()
	defer m.Unlock()

	var meta model.Plugin
	if err := json.Unmarshal(manifestBytes, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse plugin manifest: %w", err)
	}
	if meta.ID == "" || meta.Name == "" || meta.Version == "" {
		return nil, fmt.Errorf("plugin manifest is missing required fields (id, name, version)")
	}

	// 1. 查找匹配的处理器并检查插件类型
	var handler PluginHandler
	for _, h := range m.handlers {
		if strings.HasPrefix(meta.ID, h.Prefix()) {
			handler = h
			break
		}
	}
	if handler == nil {
		return nil, fmt.Errorf("unsupported plugin type for ID '%s'", meta.ID)
	}

	if _, exists := m.plugins[meta.ID]; exists {
		return nil, fmt.Errorf("plugin with id '%s' already exists", meta.ID)
	}

	fileName := formatPluginFileName(meta.Author, meta.ID)
	wasmPath := filepath.Join(m.pluginDir, fileName)
	if err := os.WriteFile(wasmPath, wasmBytes, 0644); err != nil {
		return nil, fmt.Errorf("failed to save wasm file: %w", err)
	}

	pluginModel := &model.Plugin{
		ID:          meta.ID,
		Name:        meta.Name,
		Version:     meta.Version,
		Author:      meta.Author,
		Description: meta.Description,
		IconURL:     meta.IconURL,
		SourceURL:   sourceURL,
		WasmPath:    wasmPath,
	}

	// 先存入数据库，初始状态为 'inactive'
	if err := db.CreatePlugin(ctx, pluginModel); err != nil {
		os.Remove(wasmPath)
		return nil, fmt.Errorf("failed to save plugin metadata to database: %w", err)
	}
	log.Infof("Plugin '%s' metadata saved to database with status: inactive.", pluginModel.ID)

	pluginInfo := &PluginInfo{Plugin: pluginModel, handler: handler}
	m.plugins[pluginInfo.ID] = pluginInfo

	// 使用找到的处理器进行注册
	if err := handler.Register(ctx, pluginInfo); err != nil {
		// 注册失败，更新数据库状态
		log.Errorf("Failed to register newly installed plugin '%s': %v", pluginInfo.ID, err)
		pluginInfo.Status = model.StatusError
		pluginInfo.Message = err.Error()
		if dbErr := db.UpdatePluginStatus(ctx, pluginInfo.ID, model.StatusError, err.Error()); dbErr != nil {
			log.Errorf("Failed to update error status for plugin '%s' in database: %v", pluginInfo.ID, dbErr)
		}
	} else {
		// 注册成功，更新状态
		pluginInfo.Status = model.StatusActive
		pluginInfo.Message = ""
		if dbErr := db.UpdatePluginStatus(ctx, pluginInfo.ID, model.StatusActive, ""); dbErr != nil {
			log.Errorf("Failed to update active status for plugin '%s' in database: %v", pluginInfo.ID, dbErr)
		}
	}

	return pluginInfo, nil
}

// Uninstall 卸载一个插件
func (m *Manager) Uninstall(ctx context.Context, pluginID string) error {
	m.Lock()
	defer m.Unlock()

	plugin, ok := m.plugins[pluginID]
	if !ok {
		return fmt.Errorf("plugin with ID '%s' not found", pluginID)
	}

	// 1. 使用对应的处理器进行注销
	if plugin.handler != nil {
		if err := plugin.handler.Unregister(ctx, plugin); err != nil {
			// 即便注销失败，也要继续删除流程
			log.Warnf("Failed to unregister plugin '%s', but continuing with uninstallation: %v", pluginID, err)
		}
	}

	// 2. 关闭插件内部资源 (如果 driver 实例存在)
	if plugin.driver != nil {
		if err := plugin.driver.Close(ctx); err != nil {
			log.Warnf("Error closing driver resources for plugin %s: %v", pluginID, err)
		}
	}

	// 3. 从数据库删除
	if err := db.DeletePluginByID(ctx, pluginID); err != nil {
		return fmt.Errorf("failed to delete plugin '%s' from database: %w", pluginID, err)
	}

	// 4. 删除文件
	if err := os.Remove(plugin.WasmPath); err != nil && !os.IsNotExist(err) {
		log.Warnf("Failed to remove wasm file %s, but database entry was removed: %v", plugin.WasmPath, err)
	}

	// 5. 从内存中删除
	delete(m.plugins, pluginID)
	log.Infof("Plugin '%s' has been successfully uninstalled.", pluginID)
	return nil
}

// CheckForUpdate 检查单个指定插件的更新。
// 如果有可用更新，则返回新版本号；否则返回空字符串。
func (m *Manager) CheckForUpdate(ctx context.Context, pluginID string) (string, error) {
	m.RLock()
	plugin, ok := m.plugins[pluginID]
	m.RUnlock()

	if !ok {
		return "", fmt.Errorf("plugin with ID '%s' not found", pluginID)
	}

	if !strings.HasPrefix(plugin.SourceURL, "https://github.com/") {
		return "", fmt.Errorf("only plugins installed from GitHub can be checked for updates")
	}

	latestVersionStr, err := m.getLatestGitHubVersionTag(ctx, plugin.SourceURL)
	if err != nil {
		return "", fmt.Errorf("failed to check for updates for plugin '%s': %w", pluginID, err)
	}

	latestVersion, err := semver.NewVersion(latestVersionStr)
	if err != nil {
		return "", fmt.Errorf("invalid latest version format '%s' for plugin '%s': %w", latestVersionStr, pluginID, err)
	}

	currentVersion, err := semver.NewVersion(plugin.Version)
	if err != nil {
		return "", fmt.Errorf("invalid current version format '%s' for plugin '%s': %w", plugin.Version, pluginID, err)
	}

	if latestVersion.Compare(*currentVersion) > 0 {
		return latestVersion.String(), nil
	}

	// 没有可用更新
	return "", nil
}

// CheckForUpdates 检查所有已安装插件的更新。
func (m *Manager) CheckForUpdates(ctx context.Context) (map[string]string, error) {
	m.RLock()
	defer m.RUnlock()

	updatesAvailable := make(map[string]string)

	for id, plugin := range m.plugins {
		if !strings.HasPrefix(plugin.SourceURL, "https://github.com/") {
			continue // 只支持检查来自 GitHub 的插件
		}

		latestVersionStr, err := m.getLatestGitHubVersionTag(ctx, plugin.SourceURL)
		if err != nil {
			log.Warnf("Failed to check for updates for plugin '%s': %v", id, err)
			continue
		}

		latestVersion, err := semver.NewVersion(latestVersionStr)
		if err != nil {
			log.Warnf("Invalid latest version format '%s' for plugin '%s': %v", latestVersionStr, id, err)
			continue
		}

		currentVersion, err := semver.NewVersion(plugin.Version)
		if err != nil {
			log.Warnf("Invalid current version format '%s' for plugin '%s': %v", plugin.Version, id, err)
			continue
		}

		// 使用 Compare 方法进行比较
		if latestVersion.Compare(*currentVersion) > 0 {
			updatesAvailable[id] = latestVersion.String()
			log.Infof("Update available for plugin '%s': %s -> %s", id, currentVersion.String(), latestVersion.String())
		}
	}

	return updatesAvailable, nil
}

// Update 更新指定的插件到最新版本。
func (m *Manager) Update(ctx context.Context, pluginID string) (*PluginInfo, error) {
	m.Lock()
	plugin, ok := m.plugins[pluginID]
	m.Unlock() // 提前解锁

	if !ok {
		return nil, fmt.Errorf("plugin with ID '%s' not found", pluginID)
	}

	if !strings.HasPrefix(plugin.SourceURL, "https://github.com/") {
		return nil, fmt.Errorf("only plugins installed from GitHub can be updated automatically")
	}

	log.Infof("Updating plugin '%s' from %s", pluginID, plugin.SourceURL)

	// 先卸载旧版本
	if err := m.Uninstall(ctx, pluginID); err != nil {
		return nil, fmt.Errorf("failed to uninstall old version of plugin '%s' during update: %w", pluginID, err)
	}

	// 重新从 GitHub 安装
	return m.Install(ctx, plugin.SourceURL)
}

// getLatestGitHubVersionTag 从 GitHub API 获取最新的 release tag 字符串。
func (m *Manager) getLatestGitHubVersionTag(ctx context.Context, repoURL string) (string, error) {
	// 规范化 URL 并解析 owner/repo
	repoURL = strings.TrimSuffix(repoURL, ".git")
	parts := strings.Split(strings.TrimPrefix(repoURL, "https://github.com/"), "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid github repo URL format: %s", repoURL)
	}
	owner, repo := parts[0], parts[1]

	// 构建 API URL
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", owner, repo)

	// 创建带上下文的 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request for GitHub API: %w", err)
	}
	// 根据 GitHub API v3 的要求设置 Accept header
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	// 执行请求
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to call GitHub API at %s: %w", apiURL, err)
	}
	defer resp.Body.Close()

	// 检查响应状态码
	if resp.StatusCode != http.StatusOK {
		// 读取响应体以获取更详细的错误信息
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API returned non-200 status: %s, body: %s", resp.Status, string(body))
	}

	// 定义一个结构体来仅解析我们需要的字段 (tag_name)
	var release struct {
		TagName string `json:"tag_name"`
	}

	// 解析 JSON 响应
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to parse GitHub API response: %w", err)
	}

	if release.TagName == "" {
		return "", errors.New("no tag_name found in the latest release")
	}

	return release.TagName, nil
}

// --- 辅助函数 ---

// extractPluginFromZip 从 zip 文件中提取 plugin.json 和 .wasm 文件
func extractPluginFromZip(zipPath string) ([]byte, []byte, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, nil, err
	}
	defer r.Close()

	var manifestBytes, wasmBytes []byte

	for _, f := range r.File {
		// 忽略目录和非插件文件
		if f.FileInfo().IsDir() {
			continue
		}

		baseName := filepath.Base(f.Name)
		if baseName == "plugin.json" {
			rc, err := f.Open()
			if err != nil {
				return nil, nil, err
			}
			manifestBytes, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, nil, err
			}
		} else if strings.HasSuffix(baseName, ".wasm") {
			rc, err := f.Open()
			if err != nil {
				return nil, nil, err
			}
			wasmBytes, err = io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, nil, err
			}
		}
	}

	if manifestBytes == nil {
		return nil, nil, errors.New("manifest 'plugin.json' not found in archive")
	}
	if wasmBytes == nil {
		return nil, nil, errors.New("no .wasm file found in archive")
	}

	return manifestBytes, wasmBytes, nil
}

// downloadTempFile 将文件从 URL 下载到临时目录
func downloadTempFile(client *http.Client, url string) (*os.File, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("server returned status: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp("", "plugin-download-*.zip")
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(tmpFile, resp.Body)
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, err
	}

	// 确保内容写入磁盘
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return nil, err
	}

	tmpFile.Close()
	return tmpFile, nil
}

var nonAlphanumericRegex = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func sanitize(s string) string {
	if s == "" {
		return "unknown"
	}
	return nonAlphanumericRegex.ReplaceAllString(s, "_")
}

func formatPluginFileName(author, id string) string {
	return fmt.Sprintf("%s-%s.wasm", sanitize(author), sanitize(id))
}
