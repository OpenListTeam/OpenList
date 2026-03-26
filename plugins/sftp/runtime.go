package sftp

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	pkgsftp "github.com/pkg/sftp"
	"github.com/pkg/errors"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	listen      string
	listener    net.Listener
	proxyHeader http.Header
	conns       map[net.Conn]struct{}
	mu          sync.Mutex
}

func NewServer(cfg SFTP) (*Server, error) {
	InitHostKey()
	return &Server{
		listen: cfg.Listen,
		proxyHeader: http.Header{
			"User-Agent": {base.UserAgent},
		},
		conns: make(map[net.Conn]struct{}),
	}, nil
}

func (s *Server) Serve() error {
	listener, err := net.Listen("tcp", s.listen)
	if err != nil {
		return err
	}
	s.listener = listener
	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.trackConn(conn)
		go s.handleConn(conn)
	}
}

func (s *Server) Close() error {
	if s.listener != nil {
		_ = s.listener.Close()
		s.listener = nil
	}
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.conns = make(map[net.Conn]struct{})
	s.mu.Unlock()
	return nil
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.untrackConn(conn)
	sshConn, chans, reqs, err := ssh.NewServerConn(conn, s.serverConfig())
	if err != nil {
		utils.Log.Errorf("[SFTP] handshake failed from %s: %+v", conn.RemoteAddr(), err)
		return
	}
	defer sshConn.Close()
	go ssh.DiscardRequests(reqs)
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			utils.Log.Errorf("[SFTP] accept session failed: %+v", err)
			continue
		}
		go s.handleSession(sshConn, channel, requests)
	}
}

func (s *Server) handleSession(conn *ssh.ServerConn, channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()
	for req := range requests {
		ok := req.Type == "subsystem" && string(req.Payload[4:]) == "sftp"
		if req.WantReply {
			_ = req.Reply(ok, nil)
		}
		if !ok {
			continue
		}
		handler, err := s.newHandler(conn)
		if err != nil {
			utils.Log.Errorf("[SFTP] init handler failed: %+v", err)
			return
		}
		server := pkgsftp.NewRequestServer(channel, pkgsftp.Handlers{
			FileGet:  handler,
			FilePut:  handler,
			FileCmd:  handler,
			FileList: handler,
		})
		if err := server.Serve(); err != nil && !errors.Is(err, io.EOF) {
			utils.Log.Errorf("[SFTP] request server failed: %+v", err)
		}
		_ = server.Close()
		return
	}
}

func (s *Server) newHandler(conn *ssh.ServerConn) (*Handler, error) {
	userObj, err := op.GetUserByName(conn.User())
	if err != nil {
		return nil, err
	}
	return &Handler{
		user:        userObj,
		metaPass:    "",
		clientIP:    conn.RemoteAddr().String(),
		proxyHeader: s.proxyHeader,
	}, nil
}

func (s *Server) serverConfig() *ssh.ServerConfig {
	var pwdAuth func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error)
	if !setting.GetBool(conf.SFTPDisablePasswordLogin) {
		pwdAuth = s.PasswordAuth
	}
	serverConfig := &ssh.ServerConfig{
		NoClientAuth:         true,
		NoClientAuthCallback: s.NoClientAuth,
		PasswordCallback:     pwdAuth,
		PublicKeyCallback:    s.PublicKeyAuth,
		AuthLogCallback:      s.AuthLogCallback,
		BannerCallback:       s.GetBanner,
	}
	for _, k := range SSHSigners {
		serverConfig.AddHostKey(k)
	}
	return serverConfig
}

func (s *Server) NoClientAuth(conn ssh.ConnMetadata) (*ssh.Permissions, error) {
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

func (s *Server) PasswordAuth(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
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

func (s *Server) PublicKeyAuth(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
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

func (s *Server) AuthLogCallback(conn ssh.ConnMetadata, method string, err error) {
	ip := conn.RemoteAddr().String()
	if err == nil {
		utils.Log.Infof("[SFTP] %s(%s) logged in via %s", conn.User(), ip, method)
	} else if method != "none" {
		utils.Log.Infof("[SFTP] %s(%s) tries logging in via %s but with error: %s", conn.User(), ip, method, err)
	}
}

func (s *Server) GetBanner(_ ssh.ConnMetadata) string {
	return setting.GetStr(conf.Announcement)
}

func (s *Server) trackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[conn] = struct{}{}
}

func (s *Server) untrackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}
