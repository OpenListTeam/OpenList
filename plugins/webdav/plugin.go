package webdav

import (
	"fmt"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
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
	if p.conf.KeyFile, err = iplugin.StringValue(config, "key_file", p.conf.KeyFile); err != nil {
		return err
	}
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
		certFile := p.conf.CertFile
		if certFile == "" {
			certFile = conf.Conf.Scheme.CertFile
		}
		keyFile := p.conf.KeyFile
		if keyFile == "" {
			keyFile = conf.Conf.Scheme.KeyFile
		}
		return p.server.ListenAndServeTLS(certFile, keyFile)
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

func init() {
	iplugin.RegisterPlugin("webdav", func() iplugin.Plugin {
		return &WebDAVPlugin{}
	})
}
