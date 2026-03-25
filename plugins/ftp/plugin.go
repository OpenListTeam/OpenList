package ftp

import (
	"fmt"

	iplugin "github.com/OpenListTeam/OpenList/v4/internal/plugin"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	ftpserver "github.com/fclairamb/ftpserverlib"
)

type FTPPlugin struct {
	driver *MainDriver
	server *ftpserver.FtpServer
	conf   FTP
}

func (p *FTPPlugin) Name() string {
	return "ftp"
}

func (p *FTPPlugin) Init(config map[string]any) error {
	var err error
	p.conf = FTP{
		Listen:                  ":5221",
		FindPasvPortAttempts:    50,
		ActiveTransferPortNon20: false,
		IdleTimeout:             900,
		ConnectionTimeout:       30,
		DisableActiveMode:       false,
		DefaultTransferBinary:   false,
		EnableActiveConnIPCheck: true,
		EnablePasvConnIPCheck:   true,
	}
	if p.conf.Listen, err = iplugin.StringValue(config, "listen", p.conf.Listen); err != nil {
		return err
	}
	if p.conf.FindPasvPortAttempts, err = iplugin.IntValue(config, "find_pasv_port_attempts", p.conf.FindPasvPortAttempts); err != nil {
		return err
	}
	if p.conf.ActiveTransferPortNon20, err = iplugin.BoolValue(config, "active_transfer_port_non_20", p.conf.ActiveTransferPortNon20); err != nil {
		return err
	}
	if p.conf.IdleTimeout, err = iplugin.IntValue(config, "idle_timeout", p.conf.IdleTimeout); err != nil {
		return err
	}
	if p.conf.ConnectionTimeout, err = iplugin.IntValue(config, "connection_timeout", p.conf.ConnectionTimeout); err != nil {
		return err
	}
	if p.conf.DisableActiveMode, err = iplugin.BoolValue(config, "disable_active_mode", p.conf.DisableActiveMode); err != nil {
		return err
	}
	if p.conf.DefaultTransferBinary, err = iplugin.BoolValue(config, "default_transfer_binary", p.conf.DefaultTransferBinary); err != nil {
		return err
	}
	if p.conf.EnableActiveConnIPCheck, err = iplugin.BoolValue(config, "enable_active_conn_ip_check", p.conf.EnableActiveConnIPCheck); err != nil {
		return err
	}
	if p.conf.EnablePasvConnIPCheck, err = iplugin.BoolValue(config, "enable_pasv_conn_ip_check", p.conf.EnablePasvConnIPCheck); err != nil {
		return err
	}
	return nil
}

func (p *FTPPlugin) Start() error {
	driver, err := NewMainDriver(p.conf)
	if err != nil {
		return err
	}
	p.driver = driver
	utils.Log.Infof("start ftp server on %s", p.conf.Listen)
	fmt.Printf("start ftp server on %s\n", p.conf.Listen)
	p.server = ftpserver.NewFtpServer(driver)
	return p.server.ListenAndServe()
}

func (p *FTPPlugin) Stop() error {
	if p.driver != nil {
		p.driver.Stop()
		p.driver = nil
	}
	if p.server == nil {
		return nil
	}
	err := p.server.Stop()
	p.server = nil
	return err
}

var _ iplugin.Plugin = (*FTPPlugin)(nil)

func init() {
	iplugin.RegisterPlugin("ftp", func() iplugin.Plugin { return &FTPPlugin{} })
}
