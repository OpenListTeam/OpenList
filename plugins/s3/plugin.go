package s3

import (
	"context"
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

type S3Plugin struct {
	server *http.Server
	conf   *S3
}

func (p *S3Plugin) Name() string {
	return "s3"
}

func (p *S3Plugin) Init(config map[string]any) error {
	p.conf = &S3{
		Address: "0.0.0.0",
		Port:    5246,
		SSL:     false,
	}
	var err error
	if p.conf.Address, err = iplugin.StringValue(config, "address", p.conf.Address); err != nil {
		return err
	}
	if p.conf.Port, err = iplugin.IntValue(config, "port", p.conf.Port); err != nil {
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

func (p *S3Plugin) Start() error {
	if p.conf.Port == -1 {
		return nil
	}
	h, err := NewServer(context.Background())
	if err != nil {
		return err
	}
	r := gin.New()
	r.Use(gin.LoggerWithWriter(log.StandardLogger().Out), gin.RecoveryWithWriter(log.StandardLogger().Out))
	r.Any("/*path", gin.WrapH(h))
	r.Any("/", gin.WrapH(h))
	addr := fmt.Sprintf("%s:%d", p.conf.Address, p.conf.Port)
	utils.Log.Infof("start S3 server @ %s", addr)
	fmt.Printf("start S3 server @ %s\n", addr)
	p.server = &http.Server{Addr: addr, Handler: r}
	if p.conf.SSL {
		return p.server.ListenAndServeTLS(p.conf.CertFile, p.conf.KeyFile)
	}
	return p.server.ListenAndServe()
}

func (p *S3Plugin) Stop() error {
	if p.server == nil {
		return nil
	}
	err := p.server.Close()
	p.server = nil
	return err
}

var _ iplugin.Plugin = (*S3Plugin)(nil)

func init() {
	iplugin.RegisterPlugin("s3", func() iplugin.Plugin {
		return &S3Plugin{}
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
