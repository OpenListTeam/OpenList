package sftp

import (
	"fmt"

	iplugin "github.com/OpenListTeam/OpenList/v4/internal/plugin"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/sftpd-openlist"
)

type SFTPPlugin struct {
	driver *Driver
	server *sftpd.SftpServer
	conf   SFTP
}

func (p *SFTPPlugin) Name() string {
	return "sftp"
}

func (p *SFTPPlugin) Init(config map[string]any) error {
	var err error
	p.conf = SFTP{Listen: ":5222"}
	p.conf.Listen, err = iplugin.StringValue(config, "listen", p.conf.Listen)
	return err
}

func (p *SFTPPlugin) Start() error {
	driver, err := NewDriver(p.conf)
	if err != nil {
		return err
	}
	p.driver = driver
	utils.Log.Infof("start sftp server on %s", p.conf.Listen)
	fmt.Printf("start sftp server on %s\n", p.conf.Listen)
	p.server = sftpd.NewSftpServer(driver)
	return p.server.RunServer()
}

func (p *SFTPPlugin) Stop() error {
	if p.server == nil {
		return nil
	}
	err := p.server.Close()
	p.server = nil
	p.driver = nil
	return err
}

func init() {
	iplugin.RegisterPlugin("sftp", func() iplugin.Plugin {
		return &SFTPPlugin{}
	})
}
