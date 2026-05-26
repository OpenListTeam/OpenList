package static

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	"os"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	internalfs "github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/public"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

type ManifestIcon struct {
	Src   string `json:"src"`
	Sizes string `json:"sizes"`
	Type  string `json:"type"`
}

type Manifest struct {
	Display  string         `json:"display"`
	Scope    string         `json:"scope"`
	StartURL string         `json:"start_url"`
	Name     string         `json:"name"`
	Icons    []ManifestIcon `json:"icons"`
}

var static iofs.FS

func initStatic() {
	utils.Log.Debug("Initializing static file system...")
	if conf.Conf.DistDir == "" {
		dist, err := iofs.Sub(public.Public, "dist")
		if err != nil {
			utils.Log.Fatalf("failed to read dist dir: %v", err)
		}
		static = dist
		utils.Log.Debug("Using embedded dist directory")
		return
	}
	static = os.DirFS(conf.Conf.DistDir)
	utils.Log.Infof("Using custom dist directory: %s", conf.Conf.DistDir)
}

func replaceStrings(content string, replacements map[string]string) string {
	for old, new := range replacements {
		content = strings.Replace(content, old, new, 1)
	}
	return content
}

func initIndex(siteConfig SiteConfig) {
	utils.Log.Debug("Initializing index.html...")
	// dist_dir is empty and cdn is not empty, and web_version is empty or beta or dev or rolling
	if conf.Conf.DistDir == "" && conf.Conf.Cdn != "" && (conf.WebVersion == "" || conf.WebVersion == "beta" || conf.WebVersion == "dev" || conf.WebVersion == "rolling") {
		utils.Log.Infof("Fetching index.html from CDN: %s/index.html...", siteConfig.Cdn)
		resp, err := base.RestyClient.R().
			SetHeader("Accept", "text/html").
			Get(fmt.Sprintf("%s/index.html", siteConfig.Cdn))
		if err != nil {
			utils.Log.Fatalf("failed to fetch index.html from CDN: %v", err)
		}
		if resp.StatusCode() != http.StatusOK {
			utils.Log.Fatalf("failed to fetch index.html from CDN, status code: %d", resp.StatusCode())
		}
		conf.RawIndexHtml = string(resp.Body())
		utils.Log.Info("Successfully fetched index.html from CDN")
	} else {
		utils.Log.Debug("Reading index.html from static files system...")
		indexFile, err := static.Open("index.html")
		if err != nil {
			if errors.Is(err, iofs.ErrNotExist) {
				utils.Log.Fatalf("index.html not exist, you may forget to put dist of frontend to public/dist")
			}
			utils.Log.Fatalf("failed to read index.html: %v", err)
		}
		defer func() {
			_ = indexFile.Close()
		}()
		index, err := io.ReadAll(indexFile)
		if err != nil {
			utils.Log.Fatalf("failed to read dist/index.html")
		}
		conf.RawIndexHtml = string(index)
		utils.Log.Debug("Successfully read index.html from static files system")
	}
	utils.Log.Debug("Replacing placeholders in index.html...")
	// Construct the correct manifest path based on basePath
	manifestPath := "/manifest.json"
	if siteConfig.BasePath != "/" {
		manifestPath = siteConfig.BasePath + "/manifest.json"
	}
	replaceMap := map[string]string{
		"cdn: undefined":        fmt.Sprintf("cdn: '%s'", siteConfig.Cdn),
		"base_path: undefined":  fmt.Sprintf("base_path: '%s'", siteConfig.BasePath),
		`href="/manifest.json"`: fmt.Sprintf(`href="%s"`, manifestPath),
	}
	conf.RawIndexHtml = replaceStrings(conf.RawIndexHtml, replaceMap)
	UpdateIndex()
}

func UpdateIndex() {
	utils.Log.Debug("Updating index.html with settings...")
	favicon := setting.GetStr(conf.Favicon)
	logo := strings.Split(setting.GetStr(conf.Logo), "\n")[0]
	title := setting.GetStr(conf.SiteTitle)
	customizeHead := setting.GetStr(conf.CustomizeHead)
	customizeBody := setting.GetStr(conf.CustomizeBody)
	mainColor := setting.GetStr(conf.MainColor)
	utils.Log.Debug("Applying replacements for default pages...")
	replaceMap1 := map[string]string{
		"https://res.oplist.org/logo/logo.svg": favicon,
		"https://res.oplist.org/logo/logo.png": logo,
		"Loading...":                           title,
		"main_color: undefined":                fmt.Sprintf("main_color: '%s'", mainColor),
	}
	conf.ManageHtml = replaceStrings(conf.RawIndexHtml, replaceMap1)
	utils.Log.Debug("Applying replacements for manage pages...")
	replaceMap2 := map[string]string{
		"<!-- customize head -->": customizeHead,
		"<!-- customize body -->": customizeBody,
	}
	conf.IndexHtml = replaceStrings(conf.ManageHtml, replaceMap2)
	utils.Log.Debug("Index.html update completed")
}

func ManifestJSON(c *gin.Context) {
	// Get site configuration to ensure consistent base path handling
	siteConfig := getSiteConfig()

	// Get site title from settings
	siteTitle := setting.GetStr(conf.SiteTitle)

	// Get logo from settings, use the first line (light theme logo)
	logoSetting := setting.GetStr(conf.Logo)
	logoUrl := strings.Split(logoSetting, "\n")[0]

	// Use base path from site config for consistency
	basePath := siteConfig.BasePath

	// Determine scope and start_url
	// PWA scope and start_url should always point to our application's base path
	// regardless of whether static resources come from CDN or local server
	scope := basePath
	startURL := basePath

	manifest := Manifest{
		Display:  "standalone",
		Scope:    scope,
		StartURL: startURL,
		Name:     siteTitle,
		Icons: []ManifestIcon{
			{
				Src:   logoUrl,
				Sizes: "512x512",
				Type:  "image/png",
			},
		},
	}

	c.Header("Content-Type", "application/json")
	c.Header("Cache-Control", "public, max-age=3600") // cache for 1 hour

	if err := json.NewEncoder(c.Writer).Encode(manifest); err != nil {
		utils.Log.Errorf("Failed to encode manifest.json: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate manifest"})
		return
	}
}

func Static(r *gin.RouterGroup, noRoute func(handlers ...gin.HandlerFunc)) {
	utils.Log.Debug("Setting up static routes...")
	siteConfig := getSiteConfig()
	initStatic()
	initIndex(siteConfig)
	folders := []string{"assets", "images", "streamer", "static"}

	if conf.Conf.Cdn == "" {
		utils.Log.Debug("Setting up static file serving...")
		r.Use(func(c *gin.Context) {
			for _, folder := range folders {
				if strings.HasPrefix(c.Request.RequestURI, fmt.Sprintf("/%s/", folder)) {
					c.Header("Cache-Control", "public, max-age=15552000")
				}
			}
		})
		for _, folder := range folders {
			sub, err := iofs.Sub(static, folder)
			if err != nil {
				utils.Log.Fatalf("can't find folder: %s", folder)
			}
			utils.Log.Debugf("Setting up route for folder: %s", folder)
			r.StaticFS(fmt.Sprintf("/%s/", folder), http.FS(sub))
		}
	} else {
		// Ensure static file redirected to CDN
		for _, folder := range folders {
			r.GET(fmt.Sprintf("/%s/*filepath", folder), func(c *gin.Context) {
				filepath := c.Param("filepath")
				c.Redirect(http.StatusFound, fmt.Sprintf("%s/%s%s", siteConfig.Cdn, folder, filepath))
			})
		}
	}

	utils.Log.Debug("Setting up catch-all route...")

	// virtualHostHandler 处理虚拟主机 Web 托管，以及默认的前端 SPA 路由
	virtualHostHandler := func(c *gin.Context) {
		// 直接从 Host 头解析域名，检查是否匹配 sharing 中的虚拟主机记录
		rawHost := c.Request.Host
		domain := stripHostPort(rawHost)
		utils.Log.Debugf("[VirtualHost] handler triggered: method=%s path=%s host=%q domain=%q",
			c.Request.Method, c.Request.URL.Path, rawHost, domain)
		if domain != "" {
			sharing, err := op.GetSharingByDomain(domain)
			if err != nil {
				utils.Log.Debugf("[VirtualHost] domain=%q not matched any sharing: %v", domain, err)
			} else if sharing != nil && len(sharing.Files) > 0 {
				utils.Log.Debugf("[VirtualHost] domain=%q matched sharing: id=%s web_hosting=%v root=%q",
					domain, sharing.ID, sharing.WebHosting, sharing.Files[0])
				// 访问码门禁：sharing.Pwd 非空时，未通过校验的请求会被门禁函数
				// 直接处理（密码输入页 / 提交表单 / 重定向），调用方需立即返回。
				if !handleSharePwdGate(c, sharing) {
					return
				}
				if sharing.WebHosting {
					// Web 托管模式：直接返回文件内容
					// 注入 nil user 到 context，CanRead(nil, ...) 直接返回 true，
					// 绕过 guest Disabled 限制，作为系统级内部访问处理
					common.GinAppendValues(c, conf.UserKey, (*model.User)(nil))
					handleWebHosting(c, sharing)
					return
				} else {
					// 路径重映射模式（伪静态）：保持地址栏不变，直接返回 SPA HTML
					// 后端 API（fs/list、fs/get、/d/、/p/）已根据 Host 头自动将路径重映射
					// 到 sharing.Files[0] 之下，前端正常请求即可看到分享目录内容
					// 注入 nil user，使 fs API 跳过 guest Disabled 限制
					common.GinAppendValues(c, conf.UserKey, (*model.User)(nil))
					utils.Log.Debugf("[VirtualHost] path remapping mode: serving SPA for domain=%q path=%q", domain, c.Request.URL.Path)
					c.Header("Content-Type", "text/html")
					c.Status(http.StatusOK)
					_, _ = c.Writer.WriteString(conf.IndexHtml)
					c.Writer.Flush()
					c.Writer.WriteHeaderNow()
					return
				}
			}
		}

		if c.Request.Method != "GET" && c.Request.Method != "POST" {
			c.Status(405)
			return
		}
		c.Header("Content-Type", "text/html")
		c.Status(200)
		if strings.HasPrefix(c.Request.URL.Path, "/@manage") {
			_, _ = c.Writer.WriteString(conf.ManageHtml)
		} else {
			_, _ = c.Writer.WriteString(conf.IndexHtml)
		}
		c.Writer.Flush()
		c.Writer.WriteHeaderNow()
	}

	// 显式注册根路径路由，确保 GET / 能被正确处理
	// gin 的 NoRoute 不会触发已注册路由前缀下的 GET /
	r.GET("/", virtualHostHandler)
	r.POST("/", virtualHostHandler)
	// NoRoute 处理其他所有未匹配路径（如 /@manage、/d/... 等 SPA 路由）
	noRoute(virtualHostHandler)
}

// indexCandidates 是 Web Hosting 模式下，访问目录时按优先级查找的索引文件名列表。
// 命中第一个存在的文件即返回；全部不存在则返回 404。
var indexCandidates = []string{
	"index.html",
	"index.htm",
	"index.mhtml",
	"index.md",
	"default.htm",
	"default.html",
	"default.mhtml",
	"default.md",
	"README.html",
	"README.htm",
	"README.mhtml",
	"README.md",
	"readme.html",
	"readme.htm",
	"readme.mhtml",
	"readme.md",
}

// handleWebHosting 处理虚拟主机（sharing）的 Web 托管请求。
// 行为：
//  1. 请求路径若指向某个具体文件（非目录），直接返回该文件内容；
//  2. 请求路径指向目录或文件不存在时，按 indexCandidates 顺序查找索引文件；
//  3. 全部未命中时返回 404。
func handleWebHosting(c *gin.Context, sharing *model.Sharing) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		utils.Log.Debugf("[VirtualHost] skip: method=%s not allowed for web hosting", c.Request.Method)
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	if len(sharing.Files) == 0 {
		utils.Log.Debugf("[VirtualHost] skip: sharing has no files")
		c.Status(http.StatusNotFound)
		return
	}
	root := sharing.Files[0]

	reqPath := c.Request.URL.Path
	// stdpath.Join 内部 Clean，会消除 .. 但仍可能逃出 root，故再做 HasPrefix 校验
	filePath := stdpath.Join(root, reqPath)
	if !strings.HasPrefix(filePath, strings.TrimRight(root, "/")+"/") && filePath != root {
		utils.Log.Warnf("[VirtualHost] path traversal rejected: root=%q reqPath=%q", root, reqPath)
		c.Status(http.StatusBadRequest)
		return
	}
	utils.Log.Debugf("[VirtualHost] handleWebHosting: reqPath=%q -> filePath=%q", reqPath, filePath)

	// 1) 直接命中文件
	if obj, err := internalfs.Get(c.Request.Context(), filePath, &internalfs.GetArgs{NoLog: true}); err == nil && !obj.IsDir() {
		utils.Log.Debugf("[VirtualHost] serving file: %q", filePath)
		serveWebHostingFile(c, filePath, obj.GetName())
		return
	}

	// 2) 目录或文件不存在：按优先级匹配索引文件
	for _, name := range indexCandidates {
		candidate := stdpath.Join(filePath, name)
		if obj, err := internalfs.Get(c.Request.Context(), candidate, &internalfs.GetArgs{NoLog: true}); err == nil && !obj.IsDir() {
			utils.Log.Debugf("[VirtualHost] serving index candidate: %q", candidate)
			serveWebHostingFile(c, candidate, obj.GetName())
			return
		}
	}

	// 3) 全部未命中
	utils.Log.Debugf("[VirtualHost] no index candidate matched for reqPath=%q under root=%q", reqPath, root)
	c.Status(http.StatusNotFound)
}

// serveWebHostingFile 通过代理方式直接返回文件内容
func serveWebHostingFile(c *gin.Context, filePath, filename string) {
	link, file, err := internalfs.Link(c.Request.Context(), filePath, model.LinkArgs{
		IP:     c.ClientIP(),
		Header: c.Request.Header,
	})
	if err != nil {
		utils.Log.Errorf("web hosting: failed to get link for %s: %v", filePath, err)
		c.Status(http.StatusInternalServerError)
		return
	}
	defer link.Close()

	// 根据文件扩展名确定正确的 Content-Type
	ext := strings.ToLower(stdpath.Ext(filename))
	contentType := mimeTypeByExt(ext)

	// .md 走服务端模板渲染（浏览器端 marked.js 渲染）。
	// 读失败或文件过大时回退为原始内容代理，不中断请求。
	if ext == ".md" {
		if data, rerr := readLinkAll(c.Request.Context(), link, file.GetSize(), renderMaxBytes); rerr == nil {
			html := renderMarkdownPreview(filename, data)
			writeRenderedHTML(c.Writer, html)
			return
		} else {
			utils.Log.Warnf("web hosting: markdown render fallback for %s: %v", filePath, rerr)
		}
	}

	// 注意：不要修改 link.Header！
	// link.Header 是请求上游存储时附加的请求头（如 Referer、Authorization 等），
	// 写入 Content-Type/Content-Disposition 会污染上游请求并触发签名失败/403。
	//
	// 正确的做法是在响应阶段通过 forceContentTypeWriter 强制覆盖响应头，
	// 同时在 ProxyIgnoreHeaders 之外通过响应头 set 直接生效。
	wrapped := &forceContentTypeWriter{
		ResponseWriter: c.Writer,
		contentType:    contentType,
		contentDisp:    "inline",
	}

	// 使用通用代理函数处理文件传输
	if err := common.Proxy(wrapped, c.Request, link, file); err != nil {
		utils.Log.Errorf("web hosting: proxy error for %s: %v", filePath, err)
	}
}

// forceContentTypeWriter 包装 http.ResponseWriter，
// 在 WriteHeader 时强制覆盖 Content-Type 和 Content-Disposition，
// 确保 HTML 等文件以正确类型返回而不是被浏览器下载
type forceContentTypeWriter struct {
	http.ResponseWriter
	contentType string
	contentDisp string
}

func (w *forceContentTypeWriter) WriteHeader(statusCode int) {
	// 上游可能返回非 2xx（如 OSS 签名异常的 403）；这种情况不要把异常响应包装成 200，
	// 也不要给浏览器一个声称是 HTML 但内容是 OSS XML 错误的响应。
	// 直接透传上游状态码，但仍覆盖 Content-Type / Content-Disposition 防止下载/乱解析。
	h := w.ResponseWriter.Header()
	if statusCode >= 200 && statusCode < 300 {
		h.Set("Content-Type", w.contentType)
		h.Set("Content-Disposition", w.contentDisp)
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *forceContentTypeWriter) Write(b []byte) (int, error) {
	return w.ResponseWriter.Write(b)
}

// mimeTypeByExt 根据文件扩展名返回 MIME 类型
func mimeTypeByExt(ext string) string {
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".mhtml", ".mht":
		// MHTML（Web 归档）。Chrome / Edge 看到 multipart/related 且无 attachment 时会
		// 调用内置的 MhtmlPageLoader 原生预览，无需服务端拆包。
		// 注意：boundary 参数是 MHTML 文件内嵌的，响应头 Content-Type 上可以不携带；
		// Chrome 会从响应 body 头部重新解析。
		return "multipart/related"
	case ".md":
		// .md 在 Web Hosting 场景下会在 serveWebHostingFile 提前走服务端渲染。
		// 这里仅作为回退路径（读失败 / 超大）提供合理的 Content-Type，
		// 让浏览器直接显示源文本而不是下载。
		return "text/markdown; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".xml":
		return "application/xml; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".ttf":
		return "font/ttf"
	case ".txt":
		return "text/plain; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

// stripHostPort removes the port from a host string.
func stripHostPort(host string) string {
	return common.StripHostPort(host)
}
