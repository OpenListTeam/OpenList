package s3

import (
	"context"
	"fmt"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
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
		Port: 5246,
		SSL:  false,
	}
	var err error
	if p.conf.Port, err = iplugin.IntValue(config, "port", p.conf.Port); err != nil {
		return err
	}
	if p.conf.SSL, err = iplugin.BoolValue(config, "ssl", p.conf.SSL); err != nil {
		return err
	}
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
	addr := fmt.Sprintf("%s:%d", conf.Conf.Scheme.Address, p.conf.Port)
	utils.Log.Infof("start S3 server @ %s", addr)
	fmt.Printf("start S3 server @ %s\n", addr)
	p.server = &http.Server{Addr: addr, Handler: r}
	if p.conf.SSL {
		return p.server.ListenAndServeTLS(conf.Conf.Scheme.CertFile, conf.Conf.Scheme.KeyFile)
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
