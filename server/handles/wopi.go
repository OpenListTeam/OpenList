package handles

import (
	"encoding/json"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/wopi"
	"github.com/gin-gonic/gin"
)

// WopiCreateSessionReq is the request for creating a WOPI session
type WopiCreateSessionReq struct {
	Path    string `json:"path" binding:"required"`
	Edit    bool   `json:"edit"`
	Service string `json:"service"`
}

// WopiCreateSession creates a new WOPI viewer session
func WopiCreateSession(c *gin.Context) {
	var req WopiCreateSessionReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	if !setting.GetBool(conf.WopiEnabled) {
		common.ErrorStrResp(c, "WOPI is not enabled", 400)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.IsGuest() && user.Disabled {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}

	// Join with user's base path (handles .. traversal detection internally)
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		meta = nil
	}

	canWrite := common.CanWrite(user, meta, reqPath) && user.CanWriteContent()
	canEdit := req.Edit && canWrite

	// Parse wopi_services
	servicesJSON := setting.GetStr(conf.WopiServices)
	if servicesJSON == "" || servicesJSON == "[]" {
		common.ErrorStrResp(c, "no WOPI services configured", 400)
		return
	}

	var services wopi.WopiServices
	if err := json.Unmarshal([]byte(servicesJSON), &services); err != nil {
		common.ErrorResp(c, fmt.Errorf("failed to parse WOPI services: %w", err), 500)
		return
	}

	// Find viewer for this extension, optionally filtered by service name
	fileExt := utils.Ext(stdpath.Base(reqPath))
	svc, viewerInfo, actionURL := services.FindViewerInService(fileExt, canEdit, req.Service)
	if actionURL == "" {
		common.ErrorStrResp(c, fmt.Sprintf("no WOPI viewer found for .%s files", fileExt), 400)
		return
	}

	// Store normalized path (with leading slash) in session
	session := wopi.GlobalSessionStore.CreateSession(reqPath, user.ID, canEdit)

	// Return session + action base URL + WOPISrc URL.
	// The frontend builds the final wopi_src and injects lang/theme/etc.
	// Use the request's actual Host header so the URL is reachable from
	// both the browser and WOPI containers (avoids localhost/site_url issues).
	siteURL := common.GetApiUrlFromRequest(c.Request)
	urlPath := strings.TrimPrefix(reqPath, "/")
	wopiSrcURL := fmt.Sprintf("%s/api/wopi/files/%s", siteURL, urlPath)

	// Extract base URL from action template (strip all template params)
	actionBaseURL := actionURL
	if idx := strings.Index(actionBaseURL, "?"); idx != -1 {
		actionBaseURL = actionBaseURL[:idx]
	}

	common.SuccessResp(c, gin.H{
		"session":      session,
		"action_url":   actionBaseURL,
		"wopi_src_url": wopiSrcURL,
		"viewer":       viewerInfo.DisplayName,
		"service_name": svc.Name,
	})
}

// WopiGetSettings returns WOPI-related settings
func WopiGetSettings(c *gin.Context) {
	common.SuccessResp(c, gin.H{
		"enabled":  setting.GetBool(conf.WopiEnabled),
		"services": setting.GetStr(conf.WopiServices),
	})
}

// WopiCheckFileInfo handles the WOPI CheckFileInfo request
func WopiCheckFileInfo(c *gin.Context) {
	rawPath := c.GetString("wopi_path")
	sessionRaw, _ := c.Get("wopi_session")
	session := sessionRaw.(*wopi.SessionCache)
	user := c.Request.Context().Value(conf.UserKey).(*model.User)

	// Verify the requested path matches the session path (prevent path confusion)
	if !utils.PathEqual(session.Path, rawPath) {
		c.Status(http.StatusForbidden)
		c.Header(wopi.ServerErrorHeader, "path mismatch")
		return
	}

	obj, err := fs.Get(c.Request.Context(), rawPath, &fs.GetArgs{})
	if err != nil {
		c.Status(http.StatusNotFound)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("file not found: %s", err))
		return
	}

	if obj.IsDir() {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "cannot open directory")
		return
	}

	siteURL := common.GetApiUrlFromRequest(c.Request)
	siteName := setting.GetStr(conf.SiteTitle)
	if siteName == "" {
		siteName = "OpenList"
	}

	info := &wopi.WopiFileInfo{
		// Required
		BaseFileName: obj.GetName(),
		Size:         obj.GetSize(),
		Version:      obj.ModTime().Format(time.RFC3339),
		// User metadata
		OwnerId:          fmt.Sprintf("%d", user.ID),
		UserId:           fmt.Sprintf("%d", user.ID),
		UserFriendlyName: user.Username,
		IsAnonymousUser:  user.IsGuest(),
		// User permissions
		ReadOnly:                !session.CanEdit,
		UserCanWrite:            session.CanEdit,
		UserCanRename:           false,
		UserCanReview:           session.CanEdit,
		UserCanNotWriteRelative: !session.CanEdit,
		// Host capabilities
		SupportsLocks:     true,
		SupportsGetLock:   true,
		SupportsUpdate:    session.CanEdit,
		SupportsRename:    false,
		SupportsReviewing: session.CanEdit,
		// PostMessage
		PostMessageOrigin:      "*",
		ClosePostMessage:       true,
		EditModePostMessage:    true,
		FileSharingPostMessage: false,
		FileVersionPostMessage: false,
		// Breadcrumb
		BreadcrumbBrandName:  siteName,
		BreadcrumbBrandUrl:   siteURL,
		BreadcrumbFolderName: stdpath.Dir(rawPath),
		BreadcrumbFolderUrl:  siteURL + "/",
		// Other
		FileNameMaxLength: 255,
		FileExtension:     stdpath.Ext(obj.GetName()),
		LastModifiedTime:  obj.ModTime().Format(time.RFC3339),
		DisablePrint:      false,
	}

	c.JSON(http.StatusOK, info)
}

// WopiGetFile handles the WOPI GetFile request
func WopiGetFile(c *gin.Context) {
	rawPath := c.GetString("wopi_path")
	sessionRaw, _ := c.Get("wopi_session")
	session := sessionRaw.(*wopi.SessionCache)

	// Verify path matches session
	if !utils.PathEqual(session.Path, rawPath) {
		c.Status(http.StatusForbidden)
		c.Header(wopi.ServerErrorHeader, "path mismatch")
		return
	}

	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		c.Status(http.StatusNotFound)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("file not found: %s", err))
		return
	}

	// Always proxy — never redirect. Collabora follows redirects but sends
	// its own auth headers, which conflict with S3 pre-signed URL signatures.
	link, file, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{
		IP:       c.ClientIP(),
		Header:   c.Request.Header,
		Redirect: false,
	})
	if err != nil {
		c.Status(http.StatusInternalServerError)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("failed to get file: %s", err))
		return
	}
	defer link.Close()

	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", file.GetName()))

	err = common.Proxy(c.Writer, c.Request, link, file)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("failed to serve file: %s", err))
		return
	}
	_ = storage
}

// WopiPutFile handles the WOPI PutFile request
func WopiPutFile(c *gin.Context) {
	rawPath := c.GetString("wopi_path")
	sessionRaw, _ := c.Get("wopi_session")
	session := sessionRaw.(*wopi.SessionCache)

	if !session.CanEdit {
		c.Status(http.StatusForbidden)
		c.Header(wopi.ServerErrorHeader, "read-only access")
		return
	}

	content := c.Request.Body
	defer content.Close()

	fileSize := c.Request.ContentLength
	if fileSize <= 0 {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "missing content")
		return
	}

	// Enforce max file size
	maxSize := setting.GetInt(conf.WopiMaxSize, 52428800)
	if maxSize > 0 && fileSize > int64(maxSize) {
		c.Status(http.StatusRequestEntityTooLarge)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("file too large: %d > %d", fileSize, maxSize))
		return
	}

	dirPath := stdpath.Dir(rawPath)
	fileName := stdpath.Base(rawPath)
	mimetype := c.GetHeader("Content-Type")
	if mimetype == "" {
		mimetype = utils.GetMimeType(fileName)
	}

	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     fileName,
			Size:     fileSize,
			Modified: time.Now(),
		},
		Reader:       content,
		Mimetype:     mimetype,
		WebPutAsTask: false,
	}

	err := fs.PutDirectly(c.Request.Context(), dirPath, s)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("failed to put file: %s", err))
		return
	}

	c.Header(wopi.ItemVersionHeader, time.Now().Format(time.RFC3339))
	c.Status(http.StatusOK)
}

// WopiModifyFile handles LOCK/UNLOCK/REFRESH_LOCK/PUT_RELATIVE
func WopiModifyFile(c *gin.Context) {
	rawPath := c.GetString("wopi_path")
	sessionRaw, _ := c.Get("wopi_session")
	session := sessionRaw.(*wopi.SessionCache)

	if !utils.PathEqual(session.Path, rawPath) {
		c.Status(http.StatusForbidden)
		c.Header(wopi.ServerErrorHeader, "path mismatch")
		return
	}

	action := c.GetHeader(wopi.OverwriteHeader)
	switch action {
	case wopi.MethodLock:
		wopiLock(c, rawPath)
	case wopi.MethodRefreshLock:
		wopiRefreshLock(c, rawPath)
	case wopi.MethodUnlock:
		wopiUnlock(c, rawPath)
	case wopi.MethodPutRelative:
		wopiPutRelative(c, rawPath, session)
	default:
		c.Status(http.StatusNotImplemented)
	}
}

func wopiLock(c *gin.Context, rawPath string) {
	lockToken := c.GetHeader(wopi.LockTokenHeader)
	if lockToken == "" {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "missing lock token")
		return
	}

	// Check if file is already locked by a different token
	existingLock := wopi.GlobalLockStore.GetLockForPath(rawPath)
	if existingLock != "" && existingLock != lockToken {
		c.Status(http.StatusConflict)
		c.Header(wopi.LockTokenHeader, existingLock)
		return
	}

	wopi.GlobalLockStore.LockPath(rawPath, lockToken)
	c.Header(wopi.LockTokenHeader, lockToken)
	c.Status(http.StatusOK)
}

func wopiUnlock(c *gin.Context, rawPath string) {
	lockToken := c.GetHeader(wopi.LockTokenHeader)
	if lockToken == "" {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "missing lock token")
		return
	}
	wopi.GlobalLockStore.UnlockPath(rawPath, lockToken)
	c.Header(wopi.LockTokenHeader, "")
	c.Status(http.StatusOK)
}

func wopiRefreshLock(c *gin.Context, rawPath string) {
	lockToken := c.GetHeader(wopi.LockTokenHeader)
	if lockToken == "" {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "missing lock token")
		return
	}
	if wopi.GlobalLockStore.RefreshPath(rawPath, lockToken) {
		c.Header(wopi.LockTokenHeader, lockToken)
		c.Status(http.StatusOK)
	} else {
		c.Status(http.StatusConflict)
		c.Header(wopi.LockTokenHeader, "")
	}
}

func wopiPutRelative(c *gin.Context, rawPath string, session *wopi.SessionCache) {
	if !session.CanEdit {
		c.Status(http.StatusForbidden)
		c.Header(wopi.ServerErrorHeader, "read-only access")
		return
	}

	suggestedTarget := c.GetHeader(wopi.SuggestedTargetHeader)
	if suggestedTarget == "" {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "missing X-WOPI-SuggestedTarget")
		return
	}

	decodedName, err := wopi.UTF7Decode(suggestedTarget)
	if err != nil {
		decodedName = suggestedTarget
	}

	if !wopi.IsValidWopiSuggestedTarget(decodedName) {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.InvalidFileNameHeader, "invalid target file name")
		return
	}

	// If starts with ".", replace extension of original file
	if strings.HasPrefix(decodedName, ".") {
		ext := stdpath.Ext(stdpath.Base(rawPath))
		baseName := strings.TrimSuffix(stdpath.Base(rawPath), ext)
		decodedName = baseName + decodedName
	}

	dirPath := stdpath.Dir(rawPath)
	newPath := utils.FixAndCleanPath(stdpath.Join(dirPath, decodedName))

	// Ensure the new path stays within the same directory
	if stdpath.Dir(newPath) != utils.FixAndCleanPath(dirPath) {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.InvalidFileNameHeader, "path traversal in filename")
		return
	}

	content := c.Request.Body
	defer content.Close()

	fileSize := c.Request.ContentLength
	if fileSize <= 0 {
		c.Status(http.StatusBadRequest)
		c.Header(wopi.ServerErrorHeader, "missing content")
		return
	}

	maxSize := setting.GetInt(conf.WopiMaxSize, 52428800)
	if maxSize > 0 && fileSize > int64(maxSize) {
		c.Status(http.StatusRequestEntityTooLarge)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("file too large: %d > %d", fileSize, maxSize))
		return
	}

	mimetype := c.GetHeader("Content-Type")
	if mimetype == "" {
		mimetype = utils.GetMimeType(decodedName)
	}

	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     decodedName,
			Size:     fileSize,
			Modified: time.Now(),
		},
		Reader:       content,
		Mimetype:     mimetype,
		WebPutAsTask: false,
	}

	err = fs.PutDirectly(c.Request.Context(), dirPath, s)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		c.Header(wopi.ServerErrorHeader, fmt.Sprintf("failed to put file: %s", err))
		return
	}

	siteURL := common.GetApiUrlFromRequest(c.Request)
	newURLPath := strings.TrimPrefix(newPath, "/")
	newWopiSrc := fmt.Sprintf("%s/api/wopi/files/%s", siteURL, newURLPath)

	c.JSON(http.StatusOK, wopi.PutRelativeResponse{
		Name: decodedName,
		Url:  newWopiSrc,
	})
}
