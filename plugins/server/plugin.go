package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	iplugin "github.com/OpenListTeam/OpenList/v4/internal/plugin"
	"github.com/pkg/errors"
	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/common"
	"github.com/OpenListTeam/OpenList/v4/pkg/middlewares"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/plugins/server/handles"
	"github.com/OpenListTeam/OpenList/v4/plugins/server/static"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type ServerPlugin struct {
	r           *gin.Engine
	conf        ServerConfig
	httpServer  *http.Server
	httpsServer *http.Server
	unixServer  *http.Server
	quicServer  *http3.Server
	unixLn      net.Listener
	stopCh      chan struct{}
	stopOnce    sync.Once
}

type ServerConfig struct {
	Address      string
	HttpPort     int
	HttpsPort    int
	ForceHttps   bool
	CertFile     string
	KeyFile      string
	UnixFile     string
	UnixFilePerm string
	EnableH2c    bool
	EnableH3     bool
}

func (s *ServerPlugin) Name() string {
	return "server"
}

func (s *ServerPlugin) Init(data map[string]any) error {
	s.conf = ServerConfig{
		Address:      "0.0.0.0",
		HttpPort:     5244,
		HttpsPort:    -1,
		ForceHttps:   false,
		CertFile:     "",
		KeyFile:      "",
		UnixFile:     "",
		UnixFilePerm: "",
		EnableH2c:    false,
		EnableH3:     false,
	}
	var err error
	if s.conf.Address, err = iplugin.StringValue(data, "address", s.conf.Address); err != nil {
		return err
	}
	if s.conf.HttpPort, err = iplugin.IntValue(data, "http_port", s.conf.HttpPort); err != nil {
		return err
	}
	if s.conf.HttpsPort, err = iplugin.IntValue(data, "https_port", s.conf.HttpsPort); err != nil {
		return err
	}
	if s.conf.ForceHttps, err = iplugin.BoolValue(data, "force_https", s.conf.ForceHttps); err != nil {
		return err
	}
	if s.conf.CertFile, err = iplugin.StringValue(data, "cert_file", s.conf.CertFile); err != nil {
		return err
	}
	s.conf.CertFile = resolvePluginPath(s.conf.CertFile)
	if s.conf.KeyFile, err = iplugin.StringValue(data, "key_file", s.conf.KeyFile); err != nil {
		return err
	}
	s.conf.KeyFile = resolvePluginPath(s.conf.KeyFile)
	if s.conf.UnixFile, err = iplugin.StringValue(data, "unix_file", s.conf.UnixFile); err != nil {
		return err
	}
	s.conf.UnixFile = resolvePluginPath(s.conf.UnixFile)
	if s.conf.UnixFilePerm, err = iplugin.StringValue(data, "unix_file_perm", s.conf.UnixFilePerm); err != nil {
		return err
	}
	if s.conf.EnableH2c, err = iplugin.BoolValue(data, "enable_h2c", s.conf.EnableH2c); err != nil {
		return err
	}
	if s.conf.EnableH3, err = iplugin.BoolValue(data, "enable_h3", s.conf.EnableH3); err != nil {
		return err
	}
	if !flags.Debug && !flags.Dev {
		gin.SetMode(gin.ReleaseMode)
	}

	s.r = gin.New()
	if conf.Conf.Log.Filter.Enable {
		s.r.Use(middlewares.FilteredLogger())
	} else {
		s.r.Use(gin.LoggerWithWriter(log.StandardLogger().Out))
	}
	s.r.Use(gin.RecoveryWithWriter(log.StandardLogger().Out))
	s.r.ContextWithFallback = true
	if !utils.SliceContains([]string{"", "/"}, conf.URL.Path) {
		s.r.GET("/", func(c *gin.Context) {
			c.Redirect(302, conf.URL.Path)
		})
	}
	Cors(s.r)
	g := s.r.Group(conf.URL.Path)
	if s.conf.HttpPort != -1 && s.conf.HttpsPort != -1 && s.conf.ForceHttps {
		s.r.Use(middlewares.ForceHttps(s.conf.HttpPort, s.conf.HttpsPort))
	}
	g.Any("/ping", func(c *gin.Context) {
		c.String(200, "pong")
	})
	g.GET("/favicon.ico", handles.Favicon)
	g.GET("/robots.txt", handles.Robots)
	g.GET("/manifest.json", static.ManifestJSON)
	g.GET("/i/:link_name", handles.Plist)
	common.SecretKey = []byte(conf.Conf.JwtSecret)
	g.Use(middlewares.StoragesLoaded)
	if conf.Conf.MaxConnections > 0 {
		g.Use(middlewares.MaxAllowed(conf.Conf.MaxConnections))
	}

	downloadLimiter := middlewares.DownloadRateLimiter(stream.ClientDownloadLimit)
	signCheck := middlewares.Down(sign.Verify)
	g.GET("/d/*path", middlewares.PathParse, signCheck, downloadLimiter, handles.Down)
	g.GET("/p/*path", middlewares.PathParse, signCheck, downloadLimiter, handles.Proxy)
	g.HEAD("/d/*path", middlewares.PathParse, signCheck, handles.Down)
	g.HEAD("/p/*path", middlewares.PathParse, signCheck, handles.Proxy)
	archiveSignCheck := middlewares.Down(sign.VerifyArchive)
	g.GET("/ad/*path", middlewares.PathParse, archiveSignCheck, downloadLimiter, handles.ArchiveDown)
	g.GET("/ap/*path", middlewares.PathParse, archiveSignCheck, downloadLimiter, handles.ArchiveProxy)
	g.GET("/ae/*path", middlewares.PathParse, archiveSignCheck, downloadLimiter, handles.ArchiveInternalExtract)
	g.HEAD("/ad/*path", middlewares.PathParse, archiveSignCheck, handles.ArchiveDown)
	g.HEAD("/ap/*path", middlewares.PathParse, archiveSignCheck, handles.ArchiveProxy)
	g.HEAD("/ae/*path", middlewares.PathParse, archiveSignCheck, handles.ArchiveInternalExtract)

	g.GET("/sd/:sid", middlewares.EmptyPathParse, middlewares.SharingIdParse, downloadLimiter, handles.SharingDown)
	g.GET("/sd/:sid/*path", middlewares.PathParse, middlewares.SharingIdParse, downloadLimiter, handles.SharingDown)
	g.HEAD("/sd/:sid", middlewares.EmptyPathParse, middlewares.SharingIdParse, handles.SharingDown)
	g.HEAD("/sd/:sid/*path", middlewares.PathParse, middlewares.SharingIdParse, handles.SharingDown)
	g.GET("/sad/:sid", middlewares.EmptyPathParse, middlewares.SharingIdParse, downloadLimiter, handles.SharingArchiveExtract)
	g.GET("/sad/:sid/*path", middlewares.PathParse, middlewares.SharingIdParse, downloadLimiter, handles.SharingArchiveExtract)
	g.HEAD("/sad/:sid", middlewares.EmptyPathParse, middlewares.SharingIdParse, handles.SharingArchiveExtract)
	g.HEAD("/sad/:sid/*path", middlewares.PathParse, middlewares.SharingIdParse, handles.SharingArchiveExtract)

	api := g.Group("/api")
	auth := api.Group("", middlewares.Auth(false))
	webauthn := api.Group("/authn", middlewares.Authn)

	api.POST("/auth/login", handles.Login)
	api.POST("/auth/login/hash", handles.LoginHash)
	api.POST("/auth/login/ldap", handles.LoginLdap)
	auth.GET("/me", handles.CurrentUser)
	auth.POST("/me/update", handles.UpdateCurrent)
	auth.GET("/me/sshkey/list", handles.ListMyPublicKey)
	auth.POST("/me/sshkey/add", handles.AddMyPublicKey)
	auth.POST("/me/sshkey/delete", handles.DeleteMyPublicKey)
	auth.POST("/auth/2fa/generate", handles.Generate2FA)
	auth.POST("/auth/2fa/verify", handles.Verify2FA)
	auth.GET("/auth/logout", handles.LogOut)

	// auth
	api.GET("/auth/sso", handles.SSOLoginRedirect)
	api.GET("/auth/sso_callback", handles.SSOLoginCallback)
	api.GET("/auth/get_sso_id", handles.SSOLoginCallback)
	api.GET("/auth/sso_get_token", handles.SSOLoginCallback)

	// webauthn
	api.GET("/authn/webauthn_begin_login", handles.BeginAuthnLogin)
	api.POST("/authn/webauthn_finish_login", handles.FinishAuthnLogin)
	webauthn.GET("/webauthn_begin_registration", handles.BeginAuthnRegistration)
	webauthn.POST("/webauthn_finish_registration", handles.FinishAuthnRegistration)
	webauthn.POST("/delete_authn", handles.DeleteAuthnLogin)
	webauthn.GET("/getcredentials", handles.GetAuthnCredentials)

	// no need auth
	public := api.Group("/public")
	public.Any("/settings", handles.PublicSettings)
	public.Any("/offline_download_tools", handles.OfflineDownloadTools)
	public.Any("/archive_extensions", handles.ArchiveExtensions)

	_fs(auth.Group("/fs"))
	fsAndShare(api.Group("/fs", middlewares.Auth(true)))
	_task(auth.Group("/task", middlewares.AuthNotGuest))
	_sharing(auth.Group("/share", middlewares.AuthNotGuest))
	admin(auth.Group("/admin", middlewares.AuthAdmin))
	if flags.Debug || flags.Dev {
		debug(g.Group("/debug"))
	}
	static.Static(g, func(handlers ...gin.HandlerFunc) {
		s.r.NoRoute(handlers...)
	})
	return nil
}

func (s *ServerPlugin) Start() error {
	s.stopCh = make(chan struct{})
	s.stopOnce = sync.Once{}
	errCh := make(chan error, 4)
	listenerCount := 0

	var httpHandler http.Handler = s.r
	if s.conf.EnableH2c {
		httpHandler = h2c.NewHandler(s.r, &http2.Server{})
	}
	if s.conf.HttpPort != -1 {
		listenerCount++
		httpBase := fmt.Sprintf("%s:%d", s.conf.Address, s.conf.HttpPort)
		fmt.Printf("start HTTP server @ %s\n", httpBase)
		utils.Log.Infof("start HTTP server @ %s", httpBase)
		s.httpServer = &http.Server{Addr: httpBase, Handler: httpHandler}
		go func() {
			if err := s.httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("start http: %w", err)
			}
		}()
	}
	if s.conf.HttpsPort != -1 {
		listenerCount++
		httpsBase := fmt.Sprintf("%s:%d", s.conf.Address, s.conf.HttpsPort)
		fmt.Printf("start HTTPS server @ %s\n", httpsBase)
		utils.Log.Infof("start HTTPS server @ %s", httpsBase)
		s.httpsServer = &http.Server{Addr: httpsBase, Handler: httpHandler}
		go func() {
			if err := s.httpsServer.ListenAndServeTLS(s.conf.CertFile, s.conf.KeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("start https: %w", err)
			}
		}()
		if s.conf.EnableH3 {
			fmt.Printf("start HTTP3 (quic) server @ %s\n", httpsBase)
			utils.Log.Infof("start HTTP3 (quic) server @ %s", httpsBase)
			s.r.Use(func(c *gin.Context) {
				if c.Request.TLS != nil {
					port := s.conf.HttpsPort
					c.Header("Alt-Svc", fmt.Sprintf("h3=\":%d\"; ma=86400", port))
				}
				c.Next()
			})
			s.quicServer = &http3.Server{Addr: httpsBase, Handler: s.r}
			go func() {
				if err := s.quicServer.ListenAndServeTLS(s.conf.CertFile, s.conf.KeyFile); err != nil && !errors.Is(err, http.ErrServerClosed) {
					errCh <- fmt.Errorf("start http3 (quic): %w", err)
				}
			}()
		}
	}
	if s.conf.UnixFile != "" {
		listenerCount++
		fmt.Printf("start unix server @ %s\n", s.conf.UnixFile)
		utils.Log.Infof("start unix server @ %s", s.conf.UnixFile)
		listener, err := net.Listen("unix", s.conf.UnixFile)
		if err != nil {
			_ = s.Stop()
			return err
		}
		s.unixLn = listener
		s.unixServer = &http.Server{Handler: httpHandler}
		if s.conf.UnixFilePerm != "" {
			mode, err := strconv.ParseUint(s.conf.UnixFilePerm, 8, 32)
			if err != nil {
				utils.Log.Errorf("failed to parse socket file permission: %+v", err)
			} else if err = os.Chmod(s.conf.UnixFile, os.FileMode(mode)); err != nil {
				utils.Log.Errorf("failed to chmod socket file: %+v", err)
			}
		}
		go func() {
			if err := s.unixServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errCh <- fmt.Errorf("start unix: %w", err)
			}
		}()
	}
	if listenerCount == 0 {
		return fmt.Errorf("server plugin has no enabled listeners")
	}

	select {
	case err := <-errCh:
		_ = s.Stop()
		return err
	case <-s.stopCh:
		return nil
	}
}

func (s *ServerPlugin) Stop() error {
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if s.quicServer != nil {
		if err := s.quicServer.Shutdown(ctx); err != nil {
			utils.Log.Errorf("failed to shutdown http3 (quic): %s", err.Error())
		}
		s.quicServer = nil
	}
	if s.unixServer != nil {
		if err := s.unixServer.Shutdown(ctx); err != nil {
			utils.Log.Errorf("failed to shutdown unix: %s", err.Error())
		}
		s.unixServer = nil
	}
	if s.unixLn != nil {
		_ = s.unixLn.Close()
		s.unixLn = nil
	}
	if s.httpsServer != nil {
		if err := s.httpsServer.Shutdown(ctx); err != nil {
			utils.Log.Errorf("failed to shutdown https: %s", err.Error())
		}
		s.httpsServer = nil
	}
	if s.httpServer != nil {
		if err := s.httpServer.Shutdown(ctx); err != nil {
			utils.Log.Errorf("failed to shutdown http: %s", err.Error())
		}
		s.httpServer = nil
	}
	return nil
}

var _ iplugin.Plugin = (*ServerPlugin)(nil)

func init() {
	iplugin.RegisterPlugin("server", func() iplugin.Plugin {
		return &ServerPlugin{}
	})
}

func resolvePluginPath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	if flags.ForceBinDir {
		executable, err := os.Executable()
		if err == nil {
			return filepath.Join(filepath.Dir(executable), path)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	return filepath.Join(wd, path)
}
