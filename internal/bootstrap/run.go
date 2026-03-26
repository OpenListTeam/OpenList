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
	"github.com/OpenListTeam/OpenList/v4/internal/plugin"
	_ "github.com/OpenListTeam/OpenList/v4/internal/plugin/all"
	"github.com/OpenListTeam/OpenList/v4/pkg/middlewares"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server"
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
	default:
		return plugin.IsRunning(t)
	}
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
	plugin.StartAll()
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
	wg.Add(1)
	go func() {
		defer wg.Done()
		plugin.StopAll()
	}()
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
