package webdav

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	iplugin "github.com/OpenListTeam/OpenList/v4/internal/plugin"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type WebDAVPlugin struct {
	conf   WebDAVConfig
	server *http.Server
}

func (p *WebDAVPlugin) Name() string {
	return "webdav"
}

func (p *WebDAVPlugin) Init(config map[string]any) error {
	var err error
	p.conf = WebDAVConfig{
		Listen: ":5288",
		SSL:    false,
	}
	if p.conf.Listen, err = iplugin.StringValue(config, "listen", p.conf.Listen); err != nil {
		return err
	}
	if p.conf.SSL, err = iplugin.BoolValue(config, "ssl", p.conf.SSL); err != nil {
		return err
	}
	if p.conf.CertFile, err = iplugin.StringValue(config, "cert_file", p.conf.CertFile); err != nil {
		return err
	}
	p.conf.CertFile = resolvePluginPath(p.conf.CertFile)
	if p.conf.KeyFile, err = iplugin.StringValue(config, "key_file", p.conf.KeyFile); err != nil {
		return err
	}
	p.conf.KeyFile = resolvePluginPath(p.conf.KeyFile)
	return nil
}

func (p *WebDAVPlugin) Start() error {
	r := gin.New()
	r.Use(gin.LoggerWithWriter(log.StandardLogger().Out), gin.RecoveryWithWriter(log.StandardLogger().Out))
	WebDav(r)
	utils.Log.Infof("start WebDAV server @ %s", p.conf.Listen)
	fmt.Printf("start WebDAV server @ %s\n", p.conf.Listen)
	p.server = &http.Server{Addr: p.conf.Listen, Handler: r}
	if p.conf.SSL {
		return p.server.ListenAndServeTLS(p.conf.CertFile, p.conf.KeyFile)
	}
	return p.server.ListenAndServe()
}

func (p *WebDAVPlugin) Stop() error {
	if p.server == nil {
		return nil
	}
	err := p.server.Close()
	p.server = nil
	return err
}

var _ iplugin.Plugin = (*WebDAVPlugin)(nil)

func init() {
	iplugin.RegisterPlugin("webdav", func() iplugin.Plugin {
		return &WebDAVPlugin{}
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
