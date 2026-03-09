package static

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	iofs "io/fs"
	"net/http"
	stdpath "path"
	"os"
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
		"cdn: undefined":                    fmt.Sprintf("cdn: '%s'", siteConfig.Cdn),
		"base_path: undefined":              fmt.Sprintf("base_path: '%s'", siteConfig.BasePath),
		`href="/manifest.json"`:             fmt.Sprintf(`href="%s"`, manifestPath),
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
		// 直接从 Host 头解析域名，检查是否匹配虚拟主机的 Web 托管请求
		rawHost := c.Request.Host
		domain := stripHostPort(rawHost)
		fmt.Printf("[VirtualHost] handler triggered: method=%s path=%s host=%q domain=%q\n",
			c.Request.Method, c.Request.URL.Path, rawHost, domain)
		utils.Log.Infof("[VirtualHost] handler triggered: method=%s path=%s host=%q domain=%q",
			c.Request.Method, c.Request.URL.Path, rawHost, domain)
		if domain != "" {
			vhost, err := op.GetVirtualHostByDomain(domain)
			if err != nil {
				utils.Log.Infof("[VirtualHost] domain=%q not found in db: %v", domain, err)
			} else {
			utils.Log.Infof("[VirtualHost] domain=%q matched vhost: id=%d enabled=%v web_hosting=%v path=%q",
					domain, vhost.ID, vhost.Enabled, vhost.WebHosting, vhost.Path)
			if vhost.Enabled && vhost.WebHosting {
					// Web 托管模式：直接返回文件内容
					// 注入 guest 用户到 context，供 internalfs.Get/Link 权限检查使用
					guest, guestErr := op.GetGuest()
					if guestErr != nil {
						utils.Log.Errorf("[VirtualHost] failed to get guest user: %v", guestErr)
						c.Status(http.StatusInternalServerError)
						return
					}
					common.GinWithValue(c, conf.UserKey, guest)
					if handleWebHosting(c, vhost) {
						return
					}
			} else if vhost.Enabled && !vhost.WebHosting {
					// 路径重映射模式（伪静态）：直接返回正常的 SPA 页面
					// 地址栏保持不变，面包屑显示用户访问的路径
					// 实际的路径映射由后端 API（fs/list、fs/get）在处理请求时完成
					utils.Log.Infof("[VirtualHost] path remapping mode: serving SPA for domain=%q path=%q", domain, c.Request.URL.Path)
					c.Header("Content-Type", "text/html")
					c.Status(200)
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

// handleWebHosting 处理虚拟主机的 Web 托管请求
// 直接将 HTML 文件内容返回给客户端，而不是走前端 SPA 路由
// 返回 true 表示已处理，false 表示未处理（继续走默认逻辑）
func handleWebHosting(c *gin.Context, vhost *model.VirtualHost) bool {
	if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
		utils.Log.Debugf("[VirtualHost] skip: method=%s not allowed for web hosting", c.Request.Method)
		return false
	}

	reqPath := c.Request.URL.Path
	// Sanitize reqPath to prevent path traversal: strip the leading slash to make
	// it a relative path, clean it, then reject if the result starts with ".."
	// (which would escape vhost.Path when joined).
	relPath := strings.TrimPrefix(reqPath, "/")
	cleanedRel := stdpath.Clean(relPath)
	if cleanedRel == "." {
		cleanedRel = ""
	}
	// Reject any path that tries to escape the virtual host root.
	if cleanedRel == ".." || strings.HasPrefix(cleanedRel, "../") || strings.HasPrefix(cleanedRel, "/") {
		utils.Log.Warnf("[VirtualHost] path traversal attempt rejected: %q", reqPath)
		c.Status(http.StatusBadRequest)
		return true
	}
	// 将请求路径映射到虚拟主机的根目录
	filePath := stdpath.Join(vhost.Path, cleanedRel)
	utils.Log.Infof("[VirtualHost] handleWebHosting: reqPath=%q -> filePath=%q", reqPath, filePath)

	// 尝试获取文件
	obj, err := internalfs.Get(c.Request.Context(), filePath, &internalfs.GetArgs{NoLog: true})
	if err == nil && !obj.IsDir() {
		// 找到文件，直接代理返回
		utils.Log.Infof("[VirtualHost] serving file: %q", filePath)
		serveWebHostingFile(c, filePath, obj.GetName())
		return true
	}
	utils.Log.Infof("[VirtualHost] file not found or is dir at %q: %v", filePath, err)

	// 如果是目录或未找到，尝试 index.html
	indexPath := stdpath.Join(filePath, "index.html")
	obj, err = internalfs.Get(c.Request.Context(), indexPath, &internalfs.GetArgs{NoLog: true})
	if err == nil && !obj.IsDir() {
		utils.Log.Infof("[VirtualHost] serving index.html: %q", indexPath)
		serveWebHostingFile(c, indexPath, "index.html")
		return true
	}
	utils.Log.Infof("[VirtualHost] index.html not found at %q: %v", indexPath, err)

	// 尝试 <path>.html（SPA 友好路由）
	if stdpath.Ext(cleanedRel) == "" && cleanedRel != "" {
		htmlPath := stdpath.Join(vhost.Path, cleanedRel+".html")
		obj, err = internalfs.Get(c.Request.Context(), htmlPath, &internalfs.GetArgs{NoLog: true})
		if err == nil && !obj.IsDir() {
			utils.Log.Infof("[VirtualHost] serving .html fallback: %q", htmlPath)
			serveWebHostingFile(c, htmlPath, stdpath.Base(htmlPath))
			return true
		}
		utils.Log.Infof("[VirtualHost] .html fallback not found at %q: %v", htmlPath, err)
	}

	utils.Log.Infof("[VirtualHost] no file matched for reqPath=%q, falling through", reqPath)
	return false
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

	// 使用包装的 ResponseWriter，在 WriteHeader 时强制覆盖 Content-Type 和 Content-Disposition
	// 这样即使 Proxy 内部的 maps.Copy 将上游响应头复制进来，我们也能在最终发送前覆盖
	wrapped := &forceContentTypeWriter{
		ResponseWriter:  c.Writer,
		contentType:     contentType,
		contentDisp:     "inline",
	}

	// 同时注入到 link.Header，供 attachHeader 路径（RangeReader/Concurrency 模式）使用
	if link.Header == nil {
		link.Header = make(http.Header)
	}
	link.Header.Set("Content-Type", contentType)
	link.Header.Set("Content-Disposition", "inline")

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
	w.ResponseWriter.Header().Set("Content-Type", w.contentType)
	w.ResponseWriter.Header().Set("Content-Disposition", w.contentDisp)
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

// stripHostPort 去掉 host 中的端口号，返回纯域名
func stripHostPort(host string) string {
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		// 确保不是 IPv6 地址（IPv6 地址用 [] 包裹）
		if !strings.Contains(host, "[") {
			return host[:idx]
		}
	}
	return host
}