package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap/data"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/OpenListTeam/sftpd-openlist"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/quic-go/quic-go/http3"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

func Init() {
	InitConfig()
	Log()
	InitDB()
	data.InitData()
	InitStreamLimit()
	InitIndex()
	InitUpgradePatch()
}

func Release() {
	db.Close()
}

var (
	running      bool
	httpSrv      *http.Server
	httpRunning  bool
	httpsSrv     *http.Server
	httpsRunning bool
	unixSrv      *http.Server
	unixRunning  bool
	quicSrv      *http3.Server
	quicRunning  bool
	s3Srv        *http.Server
	s3Running    bool
	ftpDriver    *server.FtpMainDriver
	ftpServer    *ftpserver.FtpServer
	ftpRunning   bool
	sftpDriver   *server.SftpDriver
	sftpServer   *sftpd.SftpServer
	sftpRunning  bool

	// ginEngine is the single gin engine shared by all HTTP-family listeners.
	// It is built once in Start() and never rebuilt on listener restart, so
	// the route tree and registered middlewares persist across hot-reloads.
	ginEngine *gin.Engine
	// httpHandler is ginEngine, optionally wrapped with h2c. Referenced by
	// the HTTP and Unix listeners so they pick up h2c wrapping changes.
	httpHandler http.Handler
	// h3AltSvcRegistered guards the Alt-Svc response middleware so startH3
	// can be called multiple times without registering duplicate middlewares.
	h3AltSvcRegistered bool
)

// Called by OpenList-Mobile
func IsRunning(t string) bool {
	switch t {
	case "http":
		return httpRunning
	case "https":
		return httpsRunning
	case "unix":
		return unixRunning
	case "quic":
		return quicRunning
	case "s3":
		return s3Running
	case "sftp":
		return sftpRunning
	case "ftp":
		return ftpRunning
	}
	return running
}

func Start() {
	if conf.Conf.DelayedStart != 0 {
		utils.Log.Infof("delayed start for %d seconds", conf.Conf.DelayedStart)
		time.Sleep(time.Duration(conf.Conf.DelayedStart) * time.Second)
	}
	InitOfflineDownloadTools()
	LoadStorages()
	InitTaskManager()
	if !flags.Debug && !flags.Dev {
		gin.SetMode(gin.ReleaseMode)
	}
	ginEngine = gin.New()

	// gin log
	if conf.Conf.Log.Filter.Enable {
		ginEngine.Use(middlewares.FilteredLogger())
	} else {
		ginEngine.Use(gin.LoggerWithWriter(log.StandardLogger().Out))
	}
	ginEngine.Use(gin.RecoveryWithWriter(log.StandardLogger().Out))

	server.Init(ginEngine)

	httpHandler = ginEngine
	if conf.Conf.Scheme.EnableH2c {
		httpHandler = h2c.NewHandler(ginEngine, &http2.Server{})
	}

	startHTTP()
	startHTTPS()
	startH3()
	startUnix()
	startS3()
	startFTP()
	startSFTP()

	startConfigWatcher()
	running = true
}

// startHTTP starts the HTTP listener. Idempotent: if already running, stops
// first then starts. Skipped when scheme.http_port == -1.
func startHTTP() {
	if conf.Conf.Scheme.HttpPort == -1 {
		return
	}
	if httpRunning {
		stopHTTP()
	}
	httpBase := fmt.Sprintf("%s:%d", conf.Conf.Scheme.Address, conf.Conf.Scheme.HttpPort)
	fmt.Printf("start HTTP server @ %s\n", httpBase)
	utils.Log.Infof("start HTTP server @ %s", httpBase)
	httpSrv = &http.Server{Addr: httpBase, Handler: httpHandler}
	go func() {
		httpRunning = true
		err := httpSrv.ListenAndServe()
		httpRunning = false
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			handleEndpointStartFailedHooks("http", err)
			utils.Log.Errorf("failed to start http: %s", err.Error())
		} else {
			handleEndpointShutdownHooks("http")
		}
	}()
}

// stopHTTP gracefully shuts down the HTTP listener with a 5s timeout.
// No-op if the server is not running.
func stopHTTP() {
	if httpSrv == nil {
		httpRunning = false
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		utils.Log.Error("HTTP server shutdown err: ", err)
	}
	httpSrv = nil
	httpRunning = false
}

// startHTTPS starts the HTTPS listener. Idempotent: if already running, stops
// first then starts. Skipped when scheme.https_port == -1.
func startHTTPS() {
	if conf.Conf.Scheme.HttpsPort == -1 {
		return
	}
	if httpsRunning {
		stopHTTPS()
	}
	httpsBase := fmt.Sprintf("%s:%d", conf.Conf.Scheme.Address, conf.Conf.Scheme.HttpsPort)
	fmt.Printf("start HTTPS server @ %s\n", httpsBase)
	utils.Log.Infof("start HTTPS server @ %s", httpsBase)
	httpsSrv = &http.Server{Addr: httpsBase, Handler: ginEngine}
	go func() {
		httpsRunning = true
		err := httpsSrv.ListenAndServeTLS(conf.Conf.Scheme.CertFile, conf.Conf.Scheme.KeyFile)
		httpsRunning = false
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			handleEndpointStartFailedHooks("https", err)
			utils.Log.Errorf("failed to start https: %s", err.Error())
		} else {
			handleEndpointShutdownHooks("https")
		}
	}()
}

// stopHTTPS gracefully shuts down the HTTPS listener with a 5s timeout.
// No-op if the server is not running.
func stopHTTPS() {
	if httpsSrv == nil {
		httpsRunning = false
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpsSrv.Shutdown(ctx); err != nil {
		utils.Log.Error("HTTPS server shutdown err: ", err)
	}
	httpsSrv = nil
	httpsRunning = false
}

// startH3 starts the HTTP3/QUIC listener. Idempotent: if already running,
// stops first then starts. Only starts when enable_h3 is true AND
// https_port != -1 (H3 shares the HTTPS port and cert). The Alt-Svc response
// header middleware is registered on ginEngine once (guarded by
// h3AltSvcRegistered); on stopH3 the middleware is left in place because gin
// cannot unregister middleware — it harmlessly no-ops when H3 is not desired.
func startH3() {
	if !conf.Conf.Scheme.EnableH3 || conf.Conf.Scheme.HttpsPort == -1 {
		return
	}
	if quicRunning {
		stopH3()
	}
	httpsBase := fmt.Sprintf("%s:%d", conf.Conf.Scheme.Address, conf.Conf.Scheme.HttpsPort)
	fmt.Printf("start HTTP3 (quic) server @ %s\n", httpsBase)
	utils.Log.Infof("start HTTP3 (quic) server @ %s", httpsBase)
	if !h3AltSvcRegistered {
		ginEngine.Use(func(c *gin.Context) {
			if c.Request.TLS != nil {
				port := conf.Conf.Scheme.HttpsPort
				c.Header("Alt-Svc", fmt.Sprintf("h3=\":%d\"; ma=86400", port))
			}
			c.Next()
		})
		h3AltSvcRegistered = true
	}
	quicSrv = &http3.Server{Addr: httpsBase, Handler: ginEngine}
	go func() {
		quicRunning = true
		err := quicSrv.ListenAndServeTLS(conf.Conf.Scheme.CertFile, conf.Conf.Scheme.KeyFile)
		quicRunning = false
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			handleEndpointStartFailedHooks("quic", err)
			utils.Log.Errorf("failed to start http3 (quic): %s", err.Error())
		} else {
			handleEndpointShutdownHooks("quic")
		}
	}()
}

// stopH3 gracefully shuts down the HTTP3/QUIC listener with a 5s timeout.
// No-op if the server is not running.
func stopH3() {
	if quicSrv == nil {
		quicRunning = false
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := quicSrv.Shutdown(ctx); err != nil {
		utils.Log.Error("HTTP3 (quic) server shutdown err: ", err)
	}
	quicSrv = nil
	quicRunning = false
}

// startUnix starts the Unix socket listener. Idempotent: if already running,
// stops first then starts. Skipped when scheme.unix_file == "".
func startUnix() {
	if conf.Conf.Scheme.UnixFile == "" {
		return
	}
	if unixRunning {
		stopUnix()
	}
	fmt.Printf("start unix server @ %s\n", conf.Conf.Scheme.UnixFile)
	utils.Log.Infof("start unix server @ %s", conf.Conf.Scheme.UnixFile)
	unixSrv = &http.Server{Handler: httpHandler}
	go func() {
		listener, err := net.Listen("unix", conf.Conf.Scheme.UnixFile)
		if err != nil {
			utils.Log.Errorf("failed to listen unix: %+v", err)
			return
		}
		unixRunning = true
		// set socket file permission
		mode, err := strconv.ParseUint(conf.Conf.Scheme.UnixFilePerm, 8, 32)
		if err != nil {
			utils.Log.Errorf("failed to parse socket file permission: %+v", err)
		} else {
			err = os.Chmod(conf.Conf.Scheme.UnixFile, os.FileMode(mode))
			if err != nil {
				utils.Log.Errorf("failed to chmod socket file: %+v", err)
			}
		}
		err = unixSrv.Serve(listener)
		unixRunning = false
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			handleEndpointStartFailedHooks("unix", err)
			utils.Log.Errorf("failed to start unix: %s", err.Error())
		} else {
			handleEndpointShutdownHooks("unix")
		}
	}()
}

// stopUnix gracefully shuts down the Unix socket listener with a 5s timeout.
// No-op if the server is not running.
func stopUnix() {
	if unixSrv == nil {
		unixRunning = false
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := unixSrv.Shutdown(ctx); err != nil {
		utils.Log.Error("Unix server shutdown err: ", err)
	}
	unixSrv = nil
	unixRunning = false
}

// startS3 starts the standalone S3 listener (port 5246 by default), distinct
// from the /s3 route on the main gin engine. Idempotent: if already running,
// stops first then starts. Skipped when s3.enable is false or s3.port == -1.
// A fresh gin engine is built for this listener (it does not share ginEngine).
func startS3() {
	if !conf.Conf.S3.Enable || conf.Conf.S3.Port == -1 {
		return
	}
	if s3Running {
		stopS3()
	}
	s3r := gin.New()
	s3r.Use(gin.LoggerWithWriter(log.StandardLogger().Out), gin.RecoveryWithWriter(log.StandardLogger().Out))
	server.InitS3(s3r)
	s3Base := fmt.Sprintf("%s:%d", conf.Conf.Scheme.Address, conf.Conf.S3.Port)
	fmt.Printf("start S3 server @ %s\n", s3Base)
	utils.Log.Infof("start S3 server @ %s", s3Base)
	go func() {
		s3Running = true
		var err error
		if conf.Conf.S3.SSL {
			s3Srv = &http.Server{Addr: s3Base, Handler: s3r}
			err = s3Srv.ListenAndServeTLS(conf.Conf.Scheme.CertFile, conf.Conf.Scheme.KeyFile)
		} else {
			s3Srv = &http.Server{Addr: s3Base, Handler: s3r}
			err = s3Srv.ListenAndServe()
		}
		s3Running = false
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			handleEndpointStartFailedHooks("s3", err)
			utils.Log.Errorf("failed to start s3 server: %s", err.Error())
		} else {
			handleEndpointShutdownHooks("s3")
		}
	}()
}

// stopS3 gracefully shuts down the standalone S3 listener with a 5s timeout.
// No-op if the server is not running.
func stopS3() {
	if s3Srv == nil {
		s3Running = false
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s3Srv.Shutdown(ctx); err != nil {
		utils.Log.Error("S3 server shutdown err: ", err)
	}
	s3Srv = nil
	s3Running = false
}

// startFTP starts the FTP server. Idempotent: if already running, stops first
// then starts. Skipped when ftp.enable is false or ftp.listen == "".
func startFTP() {
	if !conf.Conf.FTP.Enable || conf.Conf.FTP.Listen == "" {
		return
	}
	if ftpRunning {
		stopFTP()
	}
	var err error
	ftpDriver, err = server.NewMainDriver()
	if err != nil {
		utils.Log.Errorf("failed to start ftp driver: %s", err.Error())
		return
	}
	fmt.Printf("start ftp server on %s\n", conf.Conf.FTP.Listen)
	utils.Log.Infof("start ftp server on %s", conf.Conf.FTP.Listen)
	go func() {
		ftpServer = ftpserver.NewFtpServer(ftpDriver)
		ftpRunning = true
		err = ftpServer.ListenAndServe()
		ftpRunning = false
		if err != nil {
			handleEndpointStartFailedHooks("ftp", err)
			utils.Log.Errorf("problem ftp server listening: %s", err.Error())
		} else {
			handleEndpointShutdownHooks("ftp")
		}
	}()
}

// stopFTP gracefully shuts down the FTP server by stopping the driver (which
// closes active clients) and then the server. No-op if the server is not
// running.
func stopFTP() {
	if ftpServer == nil {
		ftpRunning = false
		ftpDriver = nil
		return
	}
	if ftpDriver != nil {
		ftpDriver.Stop()
		ftpDriver = nil
	}
	if err := ftpServer.Stop(); err != nil {
		utils.Log.Error("FTP server shutdown err: ", err)
	}
	ftpServer = nil
	ftpRunning = false
}

// startSFTP starts the SFTP server. Idempotent: if already running, stops
// first then starts. Skipped when sftp.enable is false or sftp.listen == "".
func startSFTP() {
	if !conf.Conf.SFTP.Enable || conf.Conf.SFTP.Listen == "" {
		return
	}
	if sftpRunning {
		stopSFTP()
	}
	var err error
	sftpDriver, err = server.NewSftpDriver()
	if err != nil {
		utils.Log.Errorf("failed to start sftp driver: %s", err.Error())
		return
	}
	fmt.Printf("start sftp server on %s", conf.Conf.SFTP.Listen)
	utils.Log.Infof("start sftp server on %s", conf.Conf.SFTP.Listen)
	go func() {
		sftpServer = sftpd.NewSftpServer(sftpDriver)
		sftpRunning = true
		err = sftpServer.RunServer()
		sftpRunning = false
		if err != nil {
			handleEndpointStartFailedHooks("sftp", err)
			utils.Log.Errorf("problem sftp server listening: %s", err.Error())
		} else {
			handleEndpointShutdownHooks("sftp")
		}
	}()
}

// stopSFTP gracefully shuts down the SFTP server. No-op if the server is not
// running.
func stopSFTP() {
	if sftpServer == nil {
		sftpRunning = false
		sftpDriver = nil
		return
	}
	if err := sftpServer.Close(); err != nil {
		utils.Log.Error("SFTP server shutdown err: ", err)
	}
	sftpServer = nil
	sftpDriver = nil
	sftpRunning = false
}

func Shutdown(timeout time.Duration) {
	utils.Log.Println("Shutdown server...")
	stopConfigWatcher()
	fs.ArchiveContentUploadTaskManager.RemoveAll()
	// Each stop* is internally bounded by a 5s timeout and is a no-op when
	// the corresponding server is nil. They run concurrently to mirror the
	// original Shutdown semantics. The `timeout` parameter is retained for
	// API compatibility; per-endpoint graceful shutdown uses a fixed 5s.
	var wg sync.WaitGroup
	wg.Add(7)
	go func() { defer wg.Done(); stopHTTP() }()
	go func() { defer wg.Done(); stopHTTPS() }()
	go func() { defer wg.Done(); stopH3() }()
	go func() { defer wg.Done(); stopUnix() }()
	go func() { defer wg.Done(); stopS3() }()
	go func() { defer wg.Done(); stopFTP() }()
	go func() { defer wg.Done(); stopSFTP() }()
	wg.Wait()
	utils.Log.Println("Server exit")
	running = false
}

// applyEndpointChanges restarts only the network endpoints whose config
// changed between oldConf and newConf. Endpoints that didn't change are
// left untouched. Called from reload.go during hot-reload.
func applyEndpointChanges(oldConf, newConf *conf.Config) {
	// HTTP: address, http_port, enable_h2c (affects httpHandler)
	if oldConf.Scheme.Address != newConf.Scheme.Address ||
		oldConf.Scheme.HttpPort != newConf.Scheme.HttpPort ||
		oldConf.Scheme.EnableH2c != newConf.Scheme.EnableH2c {
		// h2c change requires rebuilding httpHandler
		if oldConf.Scheme.EnableH2c != newConf.Scheme.EnableH2c {
			httpHandler = ginEngine
			if newConf.Scheme.EnableH2c {
				httpHandler = h2c.NewHandler(ginEngine, &http2.Server{})
			}
		}
		log.Info("restarting HTTP listener due to scheme change")
		stopHTTP()
		startHTTP()
	}
	// HTTPS: address, https_port, cert_file, key_file
	if oldConf.Scheme.Address != newConf.Scheme.Address ||
		oldConf.Scheme.HttpsPort != newConf.Scheme.HttpsPort ||
		oldConf.Scheme.CertFile != newConf.Scheme.CertFile ||
		oldConf.Scheme.KeyFile != newConf.Scheme.KeyFile {
		log.Info("restarting HTTPS listener due to scheme change")
		stopHTTPS()
		startHTTPS()
	}
	// H3: enable_h3 (and depends on https)
	if oldConf.Scheme.EnableH3 != newConf.Scheme.EnableH3 {
		log.Info("restarting HTTP3 listener due to enable_h3 change")
		stopH3()
		if newConf.Scheme.EnableH3 {
			startH3()
		}
	}
	// Unix: unix_file, unix_file_perm
	if oldConf.Scheme.UnixFile != newConf.Scheme.UnixFile ||
		oldConf.Scheme.UnixFilePerm != newConf.Scheme.UnixFilePerm {
		log.Info("restarting Unix listener due to scheme change")
		stopUnix()
		startUnix()
	}
	// S3 standalone: s3.enable, s3.port, s3.ssl
	if oldConf.S3.Enable != newConf.S3.Enable ||
		oldConf.S3.Port != newConf.S3.Port ||
		oldConf.S3.SSL != newConf.S3.SSL {
		log.Info("restarting S3 standalone listener due to s3 config change")
		stopS3()
		startS3()
	}
	// FTP: any ftp.* field
	if !reflect.DeepEqual(oldConf.FTP, newConf.FTP) {
		log.Info("restarting FTP server due to ftp config change")
		stopFTP()
		startFTP()
	}
	// SFTP: any sftp.* field
	if !reflect.DeepEqual(oldConf.SFTP, newConf.SFTP) {
		log.Info("restarting SFTP server due to sftp config change")
		stopSFTP()
		startSFTP()
	}
}

type EndpointStartFailedHook func(string, string)

type EndpointShutdownHook func(string)

var (
	endpointStartFailedHooks map[string]EndpointStartFailedHook
	endpointShutdownHooks    map[string]EndpointShutdownHook
)

func RegisterEndpointStartFailedHook(hook EndpointStartFailedHook) string {
	id := uuid.NewString()
	endpointStartFailedHooks[id] = hook
	return id
}

func RemoveEndpointStartFailedHook(id string) {
	delete(endpointStartFailedHooks, id)
}

func RegisterEndpointShutdownHook(hook EndpointShutdownHook) string {
	id := uuid.NewString()
	endpointShutdownHooks[id] = hook
	return id
}

func RemoveEndpointShutdownHook(id string) {
	delete(endpointShutdownHooks, id)
}

func handleEndpointStartFailedHooks(t string, err error) {
	for _, hook := range endpointStartFailedHooks {
		hook(t, err.Error())
	}
}

func handleEndpointShutdownHooks(t string) {
	for _, hook := range endpointShutdownHooks {
		hook(t)
	}
}

func init() {
	endpointShutdownHooks = make(map[string]EndpointShutdownHook)
	endpointStartFailedHooks = make(map[string]EndpointStartFailedHook)
}
