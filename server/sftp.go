package server

import (
	"context"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/ftp"
	"github.com/OpenListTeam/OpenList/v4/server/sftp"
	"github.com/OpenListTeam/sftpd-openlist"
	"github.com/pkg/errors"
	"golang.org/x/crypto/ssh"
)

type SftpDriver struct {
	proxyHeader http.Header
	config      *sftpd.Config
}

func NewSftpDriver() (*SftpDriver, error) {
	ftp.InitStage()
	sftp.InitHostKey()
	return &SftpDriver{
		proxyHeader: http.Header{
			"User-Agent": {base.UserAgent},
		},
	}, nil
}

func (d *SftpDriver) GetConfig() *sftpd.Config {
	if d.config != nil {
		return d.config
	}
	var pwdAuth func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) = nil
	if !setting.GetBool(conf.SFTPDisablePasswordLogin) {
		pwdAuth = d.PasswordAuth
	}
	serverConfig := ssh.ServerConfig{
		NoClientAuth:         true,
		NoClientAuthCallback: d.NoClientAuth,
		PasswordCallback:     pwdAuth,
		PublicKeyCallback:    d.PublicKeyAuth,
		AuthLogCallback:      d.AuthLogCallback,
		BannerCallback:       d.GetBanner,
	}
	for _, k := range sftp.SSHSigners {
		serverConfig.AddHostKey(k)
	}
	d.config = &sftpd.Config{
		ServerConfig: serverConfig,
		HostPort:     conf.Conf.SFTP.Listen,
		ErrorLogFunc: utils.Log.Error,
		// DebugLogFunc: utils.Log.Debugf,
	}
	return d.config
}

func (d *SftpDriver) GetFileSystem(sc *ssh.ServerConn) (sftpd.FileSystem, error) {
	userObj, err := op.GetUserByName(sc.User())
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, conf.UserKey, userObj)
	ctx = context.WithValue(ctx, conf.MetaPassKey, "")
	ctx = context.WithValue(ctx, conf.ClientIPKey, sc.RemoteAddr().String())
	ctx = context.WithValue(ctx, conf.ProxyHeaderKey, d.proxyHeader)
	return &sftp.DriverAdapter{FtpDriver: ftp.NewAferoAdapter(ctx)}, nil
}

func (d *SftpDriver) Close() {
}

func (d *SftpDriver) NoClientAuth(conn ssh.ConnMetadata) (*ssh.Permissions, error) {
	if conn.User() != "guest" {
		return nil, errors.New("only guest is allowed to login without authorization")
	}
	guest, err := op.GetGuest()
	if err != nil {
		return nil, err
	}
	if guest.Disabled || !guest.CanFTPAccess() {
		return nil, errors.New("user is not allowed to access via SFTP")
	}
	return nil, nil
}

func (d *SftpDriver) PasswordAuth(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
	ip := conn.RemoteAddr().String()
	count, ok := model.LoginCache.Get(ip)
	if ok && count >= model.DefaultMaxAuthRetries {
		model.LoginCache.Expire(ip, model.DefaultLockDuration)
		return nil, errors.New("Too many unsuccessful sign-in attempts have been made using an incorrect username or password, Try again later.")
	}
	pass := string(password)
	userObj, err := op.GetUserByName(conn.User())
	if err == nil {
		err = userObj.ValidateRawPassword(pass)
		if err != nil && setting.GetBool(conf.LdapLoginEnabled) && userObj.AllowLdap {
			err = common.HandleLdapLogin(conn.User(), pass)
		}
	} else if setting.GetBool(conf.LdapLoginEnabled) && model.CanFTPAccess(int32(setting.GetInt(conf.LdapDefaultPermission, 0))) {
		userObj, err = tryLdapLoginAndRegister(conn.User(), pass)
	}
	if err != nil {
		model.LoginCache.Set(ip, count+1)
		return nil, err
	}
	if userObj.Disabled || !userObj.CanFTPAccess() {
		model.LoginCache.Set(ip, count+1)
		return nil, errors.New("user is not allowed to access via SFTP")
	}
	model.LoginCache.Del(ip)
	return nil, nil
}

func (d *SftpDriver) PublicKeyAuth(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	userObj, err := op.GetUserByName(conn.User())
	if err != nil {
		return nil, err
	}
	if userObj.Disabled || !userObj.CanFTPAccess() {
		return nil, errors.New("user is not allowed to access via SFTP")
	}
	keys, _, err := op.GetSSHPublicKeyByUserId(userObj.ID, 1, -1)
	if err != nil {
		return nil, err
	}
	marshal := string(key.Marshal())
	for _, sk := range keys {
		if marshal != sk.KeyStr {
			pubKey, _, _, _, e := ssh.ParseAuthorizedKey([]byte(sk.KeyStr))
			if e != nil || marshal != string(pubKey.Marshal()) {
				continue
			}
		}
		sk.LastUsedTime = time.Now()
		_ = op.UpdateSSHPublicKey(&sk)
		return nil, nil
	}
	return nil, errors.New("public key refused")
}

func (d *SftpDriver) AuthLogCallback(conn ssh.ConnMetadata, method string, err error) {
	ip := conn.RemoteAddr().String()
	if err == nil {
		utils.Log.Infof("[SFTP] %s(%s) logged in via %s", conn.User(), ip, method)
	} else if method != "none" {
		utils.Log.Infof("[SFTP] %s(%s) tries logging in via %s but with error: %s", conn.User(), ip, method, err)
	}
}

func (d *SftpDriver) GetBanner(_ ssh.ConnMetadata) string {
	return setting.GetStr(conf.Announcement)
}
