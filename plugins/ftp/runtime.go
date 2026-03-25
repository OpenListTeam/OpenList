package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	ftpserver "github.com/fclairamb/ftpserverlib"
)

type MainDriver struct {
	settings     *ftpserver.Settings
	proxyHeader  http.Header
	clients      map[uint32]ftpserver.ClientContext
	shutdownLock sync.RWMutex
	isShutdown   bool
	tlsConfig    *tls.Config
	conf         FTP
}

func NewMainDriver(cfg FTP) (*MainDriver, error) {
	InitStage()
	transferType := ftpserver.TransferTypeASCII
	if cfg.DefaultTransferBinary {
		transferType = ftpserver.TransferTypeBinary
	}
	activeConnCheck := ftpserver.IPMatchDisabled
	if cfg.EnableActiveConnIPCheck {
		activeConnCheck = ftpserver.IPMatchRequired
	}
	pasvConnCheck := ftpserver.IPMatchDisabled
	if cfg.EnablePasvConnIPCheck {
		pasvConnCheck = ftpserver.IPMatchRequired
	}
	tlsRequired := ftpserver.ClearOrEncrypted
	if setting.GetBool(conf.FTPImplicitTLS) {
		tlsRequired = ftpserver.ImplicitEncryption
	} else if setting.GetBool(conf.FTPMandatoryTLS) {
		tlsRequired = ftpserver.MandatoryEncryption
	}
	tlsConf, err := getTLSConfig(setting.GetStr(conf.FTPTLSPrivateKeyPath), setting.GetStr(conf.FTPTLSPublicCertPath))
	if err != nil && tlsRequired != ftpserver.ClearOrEncrypted {
		return nil, fmt.Errorf("FTP mandatory TLS has been enabled, but the certificate failed to load: %w", err)
	}
	return &MainDriver{
		settings: &ftpserver.Settings{
			ListenAddr:               cfg.Listen,
			PublicHost:               lookupIP(setting.GetStr(conf.FTPPublicHost)),
			PassiveTransferPortRange: newPortMapper(setting.GetStr(conf.FTPPasvPortMap), cfg.FindPasvPortAttempts),
			ActiveTransferPortNon20:  cfg.ActiveTransferPortNon20,
			IdleTimeout:              cfg.IdleTimeout,
			ConnectionTimeout:        cfg.ConnectionTimeout,
			DisableMLSD:              false,
			DisableMLST:              false,
			DisableMFMT:              true,
			Banner:                   setting.GetStr(conf.Announcement),
			TLSRequired:              tlsRequired,
			DisableLISTArgs:          false,
			DisableSite:              false,
			DisableActiveMode:        cfg.DisableActiveMode,
			EnableHASH:               false,
			DisableSTAT:              false,
			DisableSYST:              false,
			EnableCOMB:               false,
			DefaultTransferType:      transferType,
			ActiveConnectionsCheck:   activeConnCheck,
			PasvConnectionsCheck:     pasvConnCheck,
		},
		proxyHeader: http.Header{
			"User-Agent": {base.UserAgent},
		},
		clients:   make(map[uint32]ftpserver.ClientContext),
		tlsConfig: tlsConf,
		conf:      cfg,
	}, nil
}

func (d *MainDriver) GetSettings() (*ftpserver.Settings, error) {
	return d.settings, nil
}

func (d *MainDriver) ClientConnected(cc ftpserver.ClientContext) (string, error) {
	if d.isShutdown || !d.shutdownLock.TryRLock() {
		return "", errors.New("server has shutdown")
	}
	defer d.shutdownLock.RUnlock()
	d.clients[cc.ID()] = cc
	return "OpenList FTP Endpoint", nil
}

func (d *MainDriver) ClientDisconnected(cc ftpserver.ClientContext) {
	if err := cc.Close(); err != nil {
		utils.Log.Errorf("failed to close client: %v", err)
	}
	delete(d.clients, cc.ID())
}

func (d *MainDriver) AuthUser(cc ftpserver.ClientContext, user, pass string) (ftpserver.ClientDriver, error) {
	ip := cc.RemoteAddr().String()
	count, ok := model.LoginCache.Get(ip)
	if ok && count >= model.DefaultMaxAuthRetries {
		model.LoginCache.Expire(ip, model.DefaultLockDuration)
		return nil, errors.New("Too many unsuccessful sign-in attempts have been made using an incorrect username or password, Try again later.")
	}
	var userObj *model.User
	var err error
	if user == "anonymous" || user == "guest" {
		userObj, err = op.GetGuest()
		if err != nil {
			return nil, err
		}
	} else {
		userObj, err = op.GetUserByName(user)
		if err == nil {
			err = userObj.ValidateRawPassword(pass)
			if err != nil && setting.GetBool(conf.LdapLoginEnabled) && userObj.AllowLdap {
				err = common.HandleLdapLogin(user, pass)
			}
		} else if setting.GetBool(conf.LdapLoginEnabled) && model.CanFTPAccess(int32(setting.GetInt(conf.LdapDefaultPermission, 0))) {
			userObj, err = tryLdapLoginAndRegister(user, pass)
		}
		if err != nil {
			model.LoginCache.Set(ip, count+1)
			return nil, err
		}
	}
	if userObj.Disabled || !userObj.CanFTPAccess() {
		model.LoginCache.Set(ip, count+1)
		return nil, errors.New("user is not allowed to access via FTP")
	}
	model.LoginCache.Del(ip)

	ctx := context.Background()
	ctx = context.WithValue(ctx, conf.UserKey, userObj)
	if user == "anonymous" || user == "guest" {
		ctx = context.WithValue(ctx, conf.MetaPassKey, pass)
	} else {
		ctx = context.WithValue(ctx, conf.MetaPassKey, "")
	}
	ctx = context.WithValue(ctx, conf.ClientIPKey, ip)
	ctx = context.WithValue(ctx, conf.ProxyHeaderKey, d.proxyHeader)
	return NewAferoAdapter(ctx), nil
}

func (d *MainDriver) GetTLSConfig() (*tls.Config, error) {
	if d.tlsConfig == nil {
		return nil, errors.New("TLS config not provided")
	}
	return d.tlsConfig, nil
}

func (d *MainDriver) Stop() {
	d.isShutdown = true
	d.shutdownLock.Lock()
	defer d.shutdownLock.Unlock()
	for _, client := range d.clients {
		_ = client.Close()
	}
}

func lookupIP(host string) string {
	if host == "" || net.ParseIP(host) != nil {
		return host
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		utils.Log.Errorf("given FTP public host is invalid, and the default value will be used: %v", err)
		return ""
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String()
		}
	}
	v6 := ips[0].String()
	utils.Log.Warnf("no IPv4 record looked up, %s will be used as public host, and it might do not work.", v6)
	return v6
}

type group struct {
	ExposedStart  int
	ListenedStart int
	Length        int
}

type pasvPortGetter struct {
	attempts    int
	groups      []group
	totalLength int
}

func (m *pasvPortGetter) FetchNext() (int, int, bool) {
	idxPort := rand.Intn(m.totalLength)
	for _, g := range m.groups {
		if idxPort >= g.Length {
			idxPort -= g.Length
			continue
		}
		return g.ExposedStart + idxPort, g.ListenedStart + idxPort, true
	}
	return 0, 0, false
}

func (m *pasvPortGetter) NumberAttempts() int {
	return m.attempts
}

func newPortMapper(str string, attempts int) ftpserver.PasvPortGetter {
	if str == "" {
		return nil
	}
	pasvPortMappers := strings.Split(strings.Replace(str, "\n", ",", -1), ",")
	groups := make([]group, len(pasvPortMappers))
	totalLength := 0
	convertToPorts := func(str string) (int, int, error) {
		start, end, multi := strings.Cut(str, "-")
		if multi {
			si, err := strconv.Atoi(start)
			if err != nil {
				return 0, 0, err
			}
			ei, err := strconv.Atoi(end)
			if err != nil {
				return 0, 0, err
			}
			if ei < si || ei < 1024 || si < 1024 || ei > 65535 || si > 65535 {
				return 0, 0, errors.New("invalid port")
			}
			return si, ei - si + 1, nil
		}
		ret, err := strconv.Atoi(str)
		if err != nil {
			return 0, 0, err
		}
		return ret, 1, nil
	}
	for i, mapper := range pasvPortMappers {
		var err error
		exposed, listened, mapped := strings.Cut(mapper, ":")
		for {
			if mapped {
				var es, ls, el, ll int
				es, el, err = convertToPorts(exposed)
				if err != nil {
					break
				}
				ls, ll, err = convertToPorts(listened)
				if err != nil {
					break
				}
				if el != ll {
					err = errors.New("the number of exposed ports and listened ports does not match")
					break
				}
				groups[i].ExposedStart = es
				groups[i].ListenedStart = ls
				groups[i].Length = el
				totalLength += el
			} else {
				var start, length int
				start, length, err = convertToPorts(mapper)
				groups[i].ExposedStart = start
				groups[i].ListenedStart = start
				groups[i].Length = length
				totalLength += length
			}
			break
		}
		if err != nil {
			utils.Log.Errorf("failed to convert FTP PASV port mapper %s: %v, the port mapper will be ignored.", mapper, err)
			return nil
		}
	}
	return &pasvPortGetter{attempts: attempts, groups: groups, totalLength: totalLength}
}

func getTLSConfig(keyPath, certPath string) (*tls.Config, error) {
	if keyPath == "" || certPath == "" {
		return nil, errors.New("private key or certificate is not provided")
	}
	cert, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	tlsCert, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{tlsCert}}, nil
}
