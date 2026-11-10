package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	internalnet "github.com/OpenListTeam/OpenList/v4/internal/net"
	"golang.org/x/time/rate"
)

type Feishu struct {
	model.Storage
	Addition
	mu sync.RWMutex
	s  *session
}

type session struct {
	cfg             Addition
	storageID       uint
	generation      string
	client          *http.Client
	ctx             context.Context
	cancel          context.CancelFunc
	limiter         *rate.Limiter
	tokenMu         sync.Mutex
	tenantToken     string
	tenantExpiry    time.Time
	pendingPrevious *string
	persist         func(Addition) error // supplied by tests; production uses database CAS
	exportMu        sync.Mutex
	exports         map[string]*exportEntry
	fileSizes       sync.Map // file token -> size in bytes
	writeMu         sync.Mutex
	rootMu          sync.Mutex
	rootToken       string
}

func (d *Feishu) Config() driver.Config { return config }

// Storage initialization uses formatted debug logging. Keep credentials out of it.
func (d *Feishu) String() string { return "Feishu storage" }
func (d *Feishu) GetAddition() driver.Additional {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.s == nil {
		return &d.Addition
	}
	d.s.tokenMu.Lock()
	defer d.s.tokenMu.Unlock()
	cfg := d.s.cfg
	return &cfg
}

func (d *Feishu) current() (*session, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.s == nil {
		return nil, errs.StorageNotInit
	}
	if err := d.s.ctx.Err(); err != nil {
		return nil, err
	}
	return d.s, nil
}

func normalizeRoot(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "feishu.cn" && !strings.HasSuffix(u.Hostname(), ".feishu.cn")) {
			return "", fmt.Errorf("invalid Feishu folder URL")
		}
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) != 3 || parts[0] != "drive" || parts[1] != "folder" {
			return "", fmt.Errorf("expected a Feishu cloud-space folder URL, not a wiki or document URL")
		}
		raw = parts[2]
	}
	if !validID(raw) {
		return "", fmt.Errorf("a valid Feishu root folder token is required")
	}
	return raw, nil
}

func normalizeRootForMode(raw, mode string) (string, error) {
	raw = strings.TrimSpace(raw)
	if mode == "user" {
		if raw == "" {
			return "", nil
		}
		if u, err := url.Parse(raw); err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
			(u.Hostname() == "feishu.cn" || strings.HasSuffix(u.Hostname(), ".feishu.cn")) && strings.TrimSuffix(u.Path, "/") == "/drive/me" {
			return "", nil
		}
	}
	return normalizeRoot(raw)
}

// A blank configured root is supported only for a user identity. Keep the
// resolved token separate from the configuration so renewal/restarts retain
// the administrator's choice to mount the current user's My Space.
func (s *session) root(ctx context.Context) (string, error) {
	if s.cfg.RootFolderID != "" {
		return s.cfg.RootFolderID, nil
	}
	if s.cfg.AuthMode != "user" {
		return "", fmt.Errorf("应用模式必须填写已授权的根文件夹 Token 或链接")
	}
	s.rootMu.Lock()
	defer s.rootMu.Unlock()
	if s.rootToken != "" {
		return s.rootToken, nil
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := s.json(ctx, http.MethodGet, "/drive/explorer/v2/root_folder/meta", nil, &result); err != nil {
		return "", err
	}
	if !validID(result.Token) {
		return "", fmt.Errorf("飞书未返回有效的用户根目录，请检查云空间权限或填写具体文件夹链接")
	}
	s.rootToken = result.Token
	return result.Token, nil
}

func validID(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func (d *Feishu) Init(ctx context.Context) error {
	d.mu.Lock()
	if d.s != nil {
		d.s.cancel()
		d.s.client.CloseIdleConnections()
		d.s = nil
	}
	cfg := d.Addition
	if cfg.AuthMode == "" {
		cfg.AuthMode = "tenant"
	}
	if cfg.DocumentFormat == "" {
		cfg.DocumentFormat = "docx"
	}
	if cfg.AuthMode != "tenant" && cfg.AuthMode != "user" {
		d.mu.Unlock()
		return fmt.Errorf("invalid Feishu auth_mode")
	}
	if !validCredential(cfg.AppID) || !validCredential(cfg.AppSecret) {
		d.mu.Unlock()
		return fmt.Errorf("Feishu App ID and App Secret are required")
	}
	root, err := normalizeRootForMode(cfg.RootFolderID, cfg.AuthMode)
	if err != nil {
		d.mu.Unlock()
		return err
	}
	cfg.RootFolderID = root
	if cfg.DocumentFormat != "docx" && cfg.DocumentFormat != "pdf" {
		d.mu.Unlock()
		return fmt.Errorf("invalid Feishu document_format")
	}
	if cfg.AuthMode == "user" {
		if _, err := ValidateRedirectURI(cfg.RedirectURI); err != nil {
			d.mu.Unlock()
			return err
		}
	} else {
		cfg.AccessToken = ""
		cfg.RefreshToken = ""
		cfg.TokenExpiresAt = 0
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	internalnet.SetProxyIfConfigured(transport)
	client := &http.Client{Transport: transport}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	var generation [32]byte
	if _, err := rand.Read(generation[:]); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("cannot initialize Feishu authorization")
	}
	life, cancel := context.WithCancel(context.Background())
	s := &session{cfg: cfg, storageID: d.ID, generation: hex.EncodeToString(generation[:]), client: client, ctx: life, cancel: cancel, limiter: newLimiter(), exports: make(map[string]*exportEntry)}
	d.Addition = cfg
	d.s = s
	d.mu.Unlock()
	// Saving a new OAuth storage is valid before its first authorization.
	// Reader calls still require a token; the management page shows it as pending.
	if cfg.AuthMode == "user" && cfg.RefreshToken == "" {
		return nil
	}
	ctx, stop := s.operationContext(ctx, 30*time.Second)
	defer stop()
	root, err = s.root(ctx)
	if err != nil {
		return err
	}
	_, err = s.list(ctx, root, 1) // Validate access without reading an entire directory.
	return err
}

func (d *Feishu) Drop(context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.s != nil {
		d.s.cancel()
		d.s.client.CloseIdleConnections()
		d.s = nil
	}
	return nil
}

func (s *session) operationContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	stop := context.AfterFunc(s.ctx, cancel)
	return ctx, func() { stop(); cancel() }
}

type object struct {
	model.Object
	Kind         string
	OriginalName string
}
type fileInfo struct {
	Token        string `json:"token"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	CreatedTime  string `json:"created_time"`
	ModifiedTime string `json:"modified_time"`
}
type filePage struct {
	Files         []fileInfo `json:"files"`
	HasMore       bool       `json:"has_more"`
	NextPageToken string     `json:"next_page_token"`
}

func (s *session) list(ctx context.Context, folder string, size int) (filePage, error) {
	var result filePage
	err := s.json(ctx, http.MethodGet, "/drive/v1/files?"+url.Values{"folder_token": {folder}, "page_size": {strconv.Itoa(size)}}.Encode(), nil, &result)
	return result, err
}

func (d *Feishu) GetRoot(ctx context.Context) (model.Obj, error) {
	s, err := d.current()
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.operationContext(ctx, 30*time.Second)
	defer cancel()
	root, err := s.root(ctx)
	if err != nil {
		return nil, err
	}
	return &object{Object: model.Object{ID: root, Name: "root", IsFolder: true}, Kind: "folder"}, nil
}

func unwrap(obj model.Obj) (*object, error) {
	for {
		if o, ok := obj.(*object); ok {
			if !validID(o.ID) {
				return nil, fmt.Errorf("invalid Feishu object token")
			}
			return o, nil
		}
		if wrapped, ok := obj.(model.ObjUnwrap); ok {
			obj = wrapped.Unwrap()
			continue
		}
		return nil, fmt.Errorf("invalid Feishu object")
	}
}

func (s *session) extension(kind string) string {
	switch kind {
	case "doc", "docx":
		return s.cfg.DocumentFormat
	case "sheet":
		return "xlsx"
	default:
		return ""
	}
}

func (s *session) objects(ctx context.Context, folder string) ([]model.Obj, error) {
	var files []fileInfo
	seenPages := map[string]bool{}
	seenIDs := map[string]bool{}
	next := ""
	for {
		query := url.Values{"folder_token": {folder}, "page_size": {"200"}}
		if next != "" {
			query.Set("page_token", next)
		}
		var page filePage
		if err := s.json(ctx, http.MethodGet, "/drive/v1/files?"+query.Encode(), nil, &page); err != nil {
			return nil, err
		}
		for _, f := range page.Files {
			if !validID(f.Token) || f.Name == "" || f.Type == "" {
				return nil, fmt.Errorf("invalid Feishu directory entry")
			}
			if !seenIDs[f.Token] {
				files = append(files, f)
				seenIDs[f.Token] = true
			}
		}
		if !page.HasMore {
			break
		}
		if page.NextPageToken == "" || seenPages[page.NextPageToken] {
			return nil, fmt.Errorf("invalid Feishu pagination cursor")
		}
		next = page.NextPageToken
		seenPages[next] = true
	}
	// Sort before assigning names so API page order cannot change path identity.
	sort.Slice(files, func(i, j int) bool { return files[i].Token < files[j].Token })
	result := make([]model.Obj, 0, len(files))
	names := make(map[string]int)
	for _, f := range files {
		names[s.displayName(f)]++
	}
	used := make(map[string]bool)
	for _, f := range files {
		name := s.displayName(f)
		if names[name] > 1 {
			name = withSuffix(name, f.Token)
		}
		for used[name] {
			name = withSuffix(name, f.Token)
		}
		used[name] = true
		mtime, _ := strconv.ParseInt(f.ModifiedTime, 10, 64)
		ctime, _ := strconv.ParseInt(f.CreatedTime, 10, 64)
		size := int64(-1)
		if f.Type == "folder" {
			size = 0
		}
		result = append(result, &object{Object: model.Object{ID: f.Token, Name: name, Size: size, Modified: time.Unix(mtime, 0), Ctime: time.Unix(ctime, 0), IsFolder: f.Type == "folder"}, Kind: f.Type, OriginalName: f.Name})
	}
	return result, nil
}

func withSuffix(name, token string) string {
	ext := path.Ext(name)
	return strings.TrimSuffix(name, ext) + " [" + token + "]" + ext
}
func (s *session) displayName(f fileInfo) string {
	name := strings.NewReplacer("/", "／", "\\", "＼").Replace(f.Name)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return '�'
		}
		return r
	}, name)
	if name == "." || name == ".." {
		name = "_" + name
	}
	if ext := s.extension(f.Type); ext != "" {
		name += "." + ext
	}
	return name
}

func (d *Feishu) List(ctx context.Context, dir model.Obj, _ model.ListArgs) ([]model.Obj, error) {
	s, err := d.current()
	if err != nil {
		return nil, err
	}
	o, err := unwrap(dir)
	if err != nil {
		return nil, err
	}
	if o.Kind != "folder" {
		return nil, errs.NotSupport
	}
	ctx, cancel := s.operationContext(ctx, 5*time.Minute)
	defer cancel()
	objects, err := s.objects(ctx, o.ID)
	if err != nil {
		return nil, err
	}
	for _, obj := range objects {
		file := obj.(*object)
		if file.Kind != "file" {
			continue
		}
		if size, ok := s.fileSizes.Load(file.ID); ok {
			file.Size = size.(int64)
		} else if ctx.Err() == nil {
			size, err := s.probeSize(ctx, fileDownloadEndpoint(file.ID))
			if err == nil {
				s.fileSizes.Store(file.ID, size)
				file.Size = size
			}
		}
	}
	return objects, nil
}

func (d *Feishu) MakeDir(ctx context.Context, parent model.Obj, name string) (model.Obj, error) {
	s, err := d.current()
	if err != nil {
		return nil, err
	}
	if s.cfg.ReadOnly {
		return nil, fmt.Errorf("Feishu storage is read-only")
	}
	o, err := unwrap(parent)
	if err != nil {
		return nil, err
	}
	if o.Kind != "folder" {
		return nil, errs.NotSupport
	}
	if !validName(name) {
		return nil, fmt.Errorf("invalid Feishu folder name")
	}
	ctx, cancel := s.operationContext(ctx, time.Minute)
	defer cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.checkConflict(ctx, o.ID, name); err != nil {
		return nil, err
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := s.json(ctx, http.MethodPost, "/drive/v1/files/create_folder", map[string]string{"name": name, "folder_token": o.ID}, &result); err != nil {
		return nil, err
	}
	if !validID(result.Token) {
		return nil, fmt.Errorf("Feishu returned an invalid folder token")
	}
	return &object{Object: model.Object{ID: result.Token, Name: name, IsFolder: true, Modified: time.Now()}, Kind: "folder", OriginalName: name}, nil
}

func validName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00\r\n")
}
func (s *session) checkConflict(ctx context.Context, folder, name string) error {
	objects, err := s.objects(ctx, folder)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		o := obj.(*object)
		if o.Name == name || o.OriginalName == name {
			return fmt.Errorf("a Feishu object with this name already exists; overwrite is not supported")
		}
	}
	return nil
}

var _ driver.Driver = (*Feishu)(nil)
var _ driver.GetRooter = (*Feishu)(nil)
var _ driver.MkdirResult = (*Feishu)(nil)
