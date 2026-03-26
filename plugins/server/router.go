package server

import (
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/message"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/middlewares"
	"github.com/OpenListTeam/OpenList/v4/plugins/server/handles"
	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func admin(g *gin.RouterGroup) {
	meta := g.Group("/meta")
	meta.GET("/list", handles.ListMetas)
	meta.GET("/get", handles.GetMeta)
	meta.POST("/create", handles.CreateMeta)
	meta.POST("/update", handles.UpdateMeta)
	meta.POST("/delete", handles.DeleteMeta)

	user := g.Group("/user")
	user.GET("/list", handles.ListUsers)
	user.GET("/get", handles.GetUser)
	user.POST("/create", handles.CreateUser)
	user.POST("/update", handles.UpdateUser)
	user.POST("/cancel_2fa", handles.Cancel2FAById)
	user.POST("/delete", handles.DeleteUser)
	user.POST("/del_cache", handles.DelUserCache)
	user.GET("/sshkey/list", handles.ListPublicKeys)
	user.POST("/sshkey/delete", handles.DeletePublicKey)

	storage := g.Group("/storage")
	storage.GET("/list", handles.ListStorages)
	storage.GET("/get", handles.GetStorage)
	storage.POST("/create", handles.CreateStorage)
	storage.POST("/update", handles.UpdateStorage)
	storage.POST("/delete", handles.DeleteStorage)
	storage.POST("/enable", handles.EnableStorage)
	storage.POST("/disable", handles.DisableStorage)
	storage.POST("/load_all", handles.LoadAllStorages)

	driver := g.Group("/driver")
	driver.GET("/list", handles.ListDriverInfo)
	driver.GET("/names", handles.ListDriverNames)
	driver.GET("/info", handles.GetDriverInfo)

	setting := g.Group("/setting")
	setting.GET("/get", handles.GetSetting)
	setting.GET("/list", handles.ListSettings)
	setting.POST("/save", handles.SaveSettings)
	setting.POST("/delete", handles.DeleteSetting)
	setting.POST("/default", handles.DefaultSettings)
	setting.POST("/reset_token", handles.ResetToken)
	setting.POST("/set_aria2", handles.SetAria2)
	setting.POST("/set_qbit", handles.SetQbittorrent)
	setting.POST("/set_transmission", handles.SetTransmission)
	setting.POST("/set_115", handles.Set115)
	setting.POST("/set_115_open", handles.Set115Open)
	setting.POST("/set_123_pan", handles.Set123Pan)
	setting.POST("/set_123_open", handles.Set123Open)
	setting.POST("/set_pikpak", handles.SetPikPak)
	setting.POST("/set_thunder", handles.SetThunder)
	setting.POST("/set_thunderx", handles.SetThunderX)
	setting.POST("/set_thunder_browser", handles.SetThunderBrowser)

	// retain /admin/task API to ensure compatibility with legacy automation scripts
	_task(g.Group("/task"))

	ms := g.Group("/message")
	ms.POST("/get", message.HttpInstance.GetHandle)
	ms.POST("/send", message.HttpInstance.SendHandle)

	index := g.Group("/index")
	index.POST("/build", middlewares.SearchIndex, handles.BuildIndex)
	index.POST("/update", middlewares.SearchIndex, handles.UpdateIndex)
	index.POST("/stop", middlewares.SearchIndex, handles.StopIndex)
	index.POST("/clear", middlewares.SearchIndex, handles.ClearIndex)
	index.GET("/progress", middlewares.SearchIndex, handles.GetProgress)

	scan := g.Group("/scan")
	scan.POST("/start", handles.StartManualScan)
	scan.POST("/stop", handles.StopManualScan)
	scan.GET("/progress", handles.GetManualScanProgress)
}

func fsAndShare(g *gin.RouterGroup) {
	g.Any("/list", handles.FsListSplit)
	g.Any("/get", handles.FsGetSplit)
	a := g.Group("/archive")
	a.Any("/meta", handles.FsArchiveMetaSplit)
	a.Any("/list", handles.FsArchiveListSplit)
}

func _fs(g *gin.RouterGroup) {
	g.Any("/search", middlewares.SearchIndex, handles.Search)
	g.Any("/other", handles.FsOther)
	g.Any("/dirs", handles.FsDirs)
	g.POST("/mkdir", handles.FsMkdir)
	g.POST("/rename", handles.FsRename)
	g.POST("/batch_rename", handles.FsBatchRename)
	g.POST("/regex_rename", handles.FsRegexRename)
	g.POST("/move", handles.FsMove)
	g.POST("/recursive_move", handles.FsRecursiveMove)
	g.POST("/copy", handles.FsCopy)
	g.POST("/remove", handles.FsRemove)
	g.POST("/remove_empty_directory", handles.FsRemoveEmptyDirectory)
	uploadLimiter := middlewares.UploadRateLimiter(stream.ClientUploadLimit)
	g.PUT("/put", middlewares.FsUp, uploadLimiter, handles.FsStream)
	g.PUT("/form", middlewares.FsUp, uploadLimiter, handles.FsForm)
	g.POST("/link", middlewares.AuthAdmin, handles.Link)
	// g.POST("/add_aria2", handles.AddOfflineDownload)
	// g.POST("/add_qbit", handles.AddQbittorrent)
	// g.POST("/add_transmission", handles.SetTransmission)
	g.POST("/add_offline_download", handles.AddOfflineDownload)
	g.POST("/archive/decompress", handles.FsArchiveDecompress)
	// Direct upload (client-side upload to storage)
	g.POST("/get_direct_upload_info", middlewares.FsUp, handles.FsGetDirectUploadInfo)
}

func _task(g *gin.RouterGroup) {
	handles.SetupTaskRoute(g)
}

func _sharing(g *gin.RouterGroup) {
	g.Any("/list", handles.ListSharings)
	g.GET("/get", handles.GetSharing)
	g.POST("/create", handles.CreateSharing)
	g.POST("/update", handles.UpdateSharing)
	g.POST("/delete", handles.DeleteSharing)
	g.POST("/enable", handles.SetEnableSharing(false))
	g.POST("/disable", handles.SetEnableSharing(true))
}

func Cors(r *gin.Engine) {
	config := cors.DefaultConfig()
	// config.AllowAllOrigins = true
	config.AllowOrigins = conf.Conf.Cors.AllowOrigins
	config.AllowHeaders = conf.Conf.Cors.AllowHeaders
	config.AllowMethods = conf.Conf.Cors.AllowMethods
	r.Use(cors.New(config))
}
