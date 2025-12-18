package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
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
	r := gin.New()

	// gin log
	if conf.Conf.Log.Filter.Enable {
		r.Use(middlewares.FilteredLogger())
	} else {
		r.Use(gin.LoggerWithWriter(log.StandardLogger().Out))
	}
	r.Use(gin.RecoveryWithWriter(log.StandardLogger().Out))

	server.Init(r)
	var httpHandler http.Handler = r
	if conf.Conf.Scheme.EnableH2c {
		httpHandler = h2c.NewHandler(r, &http2.Server{})
	}
	if conf.Conf.Scheme.HttpPort != -1 {
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
	if conf.Conf.Scheme.HttpsPort != -1 {
		httpsBase := fmt.Sprintf("%s:%d", conf.Conf.Scheme.Address, conf.Conf.Scheme.HttpsPort)
		fmt.Printf("start HTTPS server @ %s\n", httpsBase)
		utils.Log.Infof("start HTTPS server @ %s", httpsBase)
		httpsSrv = &http.Server{Addr: httpsBase, Handler: r}
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
		if conf.Conf.Scheme.EnableH3 {
			fmt.Printf("start HTTP3 (quic) server @ %s\n", httpsBase)
			utils.Log.Infof("start HTTP3 (quic) server @ %s", httpsBase)
			r.Use(func(c *gin.Context) {
				if c.Request.TLS != nil {
					port := conf.Conf.Scheme.HttpsPort
					c.Header("Alt-Svc", fmt.Sprintf("h3=\":%d\"; ma=86400", port))
				}
				c.Next()
			})
			quicSrv = &http3.Server{Addr: httpsBase, Handler: r}
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
	}
	if conf.Conf.Scheme.UnixFile != "" {
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
	if conf.Conf.S3.Port != -1 && conf.Conf.S3.Enable {
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
	if conf.Conf.FTP.Listen != "" && conf.Conf.FTP.Enable {
		var err error
		ftpDriver, err = server.NewMainDriver()
		if err != nil {
			utils.Log.Errorf("failed to start ftp driver: %s", err.Error())
		} else {
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
	}
	if conf.Conf.SFTP.Listen != "" && conf.Conf.SFTP.Enable {
		var err error
		sftpDriver, err = server.NewSftpDriver()
		if err != nil {
			utils.Log.Errorf("failed to start sftp driver: %s", err.Error())
		} else {
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
	}
	running = true
}

func Shutdown(timeout time.Duration) {
	utils.Log.Println("Shutdown server...")
	fs.ArchiveContentUploadTaskManager.RemoveAll()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var wg sync.WaitGroup
	if httpSrv != nil && conf.Conf.Scheme.HttpPort != -1 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := httpSrv.Shutdown(ctx); err != nil {
				utils.Log.Error("HTTP server shutdown err: ", err)
			}
			httpSrv = nil
		}()
	}
	if httpsSrv != nil && conf.Conf.Scheme.HttpsPort != -1 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := httpsSrv.Shutdown(ctx); err != nil {
				utils.Log.Error("HTTPS server shutdown err: ", err)
			}
			httpsSrv = nil
		}()
		if quicSrv != nil && conf.Conf.Scheme.EnableH3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := quicSrv.Shutdown(ctx); err != nil {
					utils.Log.Error("HTTP3 (quic) server shutdown err: ", err)
				}
				quicSrv = nil
			}()
		}
	}
	if unixSrv != nil && conf.Conf.Scheme.UnixFile != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := unixSrv.Shutdown(ctx); err != nil {
				utils.Log.Error("Unix server shutdown err: ", err)
			}
			unixSrv = nil
		}()
	}
	if s3Srv != nil && conf.Conf.S3.Port != -1 && conf.Conf.S3.Enable {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s3Srv.Shutdown(ctx); err != nil {
				utils.Log.Error("S3 server shutdown err: ", err)
			}
			s3Srv = nil
		}()
	}
	if conf.Conf.FTP.Listen != "" && conf.Conf.FTP.Enable && ftpServer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ftpDriver != nil {
				ftpDriver.Stop()
				ftpDriver = nil
			}
			if err := ftpServer.Stop(); err != nil {
				utils.Log.Error("FTP server shutdown err: ", err)
			}
			ftpServer = nil
		}()
	}
	if conf.Conf.SFTP.Listen != "" && conf.Conf.SFTP.Enable && sftpServer != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := sftpServer.Close(); err != nil {
				utils.Log.Error("SFTP server shutdown err: ", err)
			}
			sftpServer = nil
			sftpDriver = nil
		}()
	}
	wg.Wait()
	utils.Log.Println("Server exit")
	running = false
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
