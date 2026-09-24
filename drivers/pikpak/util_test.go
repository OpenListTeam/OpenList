package pikpak

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/glebarez/sqlite"
	"github.com/go-resty/resty/v2"
	"gorm.io/gorm"
)

// --- Helper function tests ---

func TestGetAction(t *testing.T) {
	tests := []struct {
		method string
		url    string
		want   string
	}{
		{"GET", "https://api-drive.mypikpak.net/drive/v1/files", "GET:/drive/v1/files"},
		{"POST", "https://user.mypikpak.net/v1/auth/signin", "POST:/v1/auth/signin"},
		{"POST", "https://user.mypikpak.net/v1/shield/captcha/init", "POST:/v1/shield/captcha/init"},
		{"GET", "https://api-drive.mypikpak.net/drive/v1/files?page_token=abc", "GET:/drive/v1/files"},
		{"POST", "https://user.mypikpak.net/v1/auth/token", "POST:/v1/auth/token"},
	}
	for _, tt := range tests {
		t.Run(tt.method+":"+tt.url, func(t *testing.T) {
			got := GetAction(tt.method, tt.url)
			if got != tt.want {
				t.Errorf("GetAction(%q, %q) = %q, want %q", tt.method, tt.url, got, tt.want)
			}
		})
	}
}

func TestGetCaptchaSign(t *testing.T) {
	c := &Common{
		ClientID:      "YNxT9w7GMdWvEOKa",
		ClientVersion: "1.53.2",
		PackageName:   "com.pikcloud.pikpak",
		DeviceID:      "test-device-id",
		Algorithms:    AndroidAlgorithms,
	}

	timestamp, sign := c.GetCaptchaSign()
	if timestamp == "" {
		t.Fatal("timestamp should not be empty")
	}
	if len(sign) != 34 {
		t.Fatalf("sign length should be 34 (\"1.\" + 32 hex), got %d: %q", len(sign), sign)
	}
	if sign[:2] != "1." {
		t.Errorf("sign should start with '1.', got %q", sign[:2])
	}
}

func TestGenerateDeviceSign(t *testing.T) {
	sign := generateDeviceSign("test-device", "com.pikcloud.pikpak")
	if len(sign) < 7 {
		t.Fatal("device sign too short")
	}
	if sign[:7] != "div101." {
		t.Errorf("device sign should start with 'div101.', got %q", sign[:7])
	}
	// Deterministic
	if sign != generateDeviceSign("test-device", "com.pikcloud.pikpak") {
		t.Error("generateDeviceSign should be deterministic")
	}
}

func TestBuildCustomUserAgent(t *testing.T) {
	ua := BuildCustomUserAgent("dev123", AndroidClientID, AndroidPackageName,
		AndroidSdkVersion, AndroidClientVersion, AndroidPackageName, "user456")
	for _, want := range []string{"ANDROID-", "clientid/", "deviceid/dev123", "usrno/user456"} {
		if !strings.Contains(ua, want) {
			t.Errorf("user agent should contain %q", want)
		}
	}
}

// --- Auth recovery behavior tests ---

func TestErrRespErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		resp      ErrResp
		wantError bool
		wantCode  int64
	}{
		{"success", ErrResp{ErrorCode: 0}, false, 0},
		{"access_token_expired_4122", ErrResp{ErrorCode: 4122, ErrorMsg: "access_token expired"}, true, 4122},
		{"access_token_expired_4121", ErrResp{ErrorCode: 4121, ErrorMsg: "access_token expired"}, true, 4121},
		{"unauthenticated_16", ErrResp{ErrorCode: 16, ErrorMsg: "unauthenticated"}, true, 16},
		{"refresh_token_invalid_4126", ErrResp{ErrorCode: 4126, ErrorMsg: "invalid_grant"}, true, 4126},
		{"captcha_expired_9", ErrResp{ErrorCode: 9, ErrorMsg: "captcha_invalid"}, true, 9},
		{"rate_limit_10", ErrResp{ErrorCode: 10, ErrorDescription: "too frequent"}, true, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotError := tt.resp.IsError()
			if gotError != tt.wantError {
				t.Errorf("IsError() = %v, want %v", gotError, tt.wantError)
			}
			if tt.resp.ErrorCode != tt.wantCode {
				t.Errorf("ErrorCode = %d, want %d", tt.resp.ErrorCode, tt.wantCode)
			}
		})
	}
}

// TestGuardClauseOnAuthURLDoesNotRefresh verifies that when the auth endpoint
// itself reports 4122, request() fails fast instead of calling refreshToken()
// (which would recurse). Real behavior, real code path: with the guard
// removed from request(), the token endpoint would be hit a second time.
func TestGuardClauseOnAuthURLDoesNotRefresh(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	m.tokenStatus = http.StatusBadRequest
	m.tokenBody = map[string]any{"error_code": 4122, "error": "access_token_expired"}

	_, err := d.request("https://user.mypikpak.net/v1/auth/token", http.MethodPost, nil, nil)
	if err == nil {
		t.Fatal("request() to an auth URL must fail on 4122 instead of refreshing")
	}
	if got := m.count(pathToken); got != 1 {
		t.Errorf("guard clause violated: token endpoint hit %d times, want exactly 1 (no refreshToken recursion)", got)
	}
	if got := m.count(pathSignin); got != 0 {
		t.Errorf("no re-login expected, got %d signin calls", got)
	}
}

// --- Integration scaffolding: in-memory DB + mock PikPak endpoints ---

var (
	setupDBOnce sync.Once
	setupDBErr  error
	rowSeq      int64
)

// setupTestDB mirrors internal/op/storage_test.go: an in-memory SQLite
// database behind internal/db, so op.MustSaveDriverStorage really persists
// and tests can assert on the saved row instead of on comments.
func setupTestDB(t *testing.T) {
	t.Helper()
	setupDBOnce.Do(func() {
		var gormDB *gorm.DB
		gormDB, setupDBErr = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
		if setupDBErr != nil {
			return
		}
		conf.Conf = conf.DefaultConfig("testdata")
		db.Init(gormDB)
	})
	if setupDBErr != nil {
		t.Fatalf("failed to set up test database: %v", setupDBErr)
	}
}

// createStorageRow inserts a fresh storage row and returns it, so that
// MustSaveDriverStorage during a test performs an UPDATE that can be read
// back afterwards.
func createStorageRow(t *testing.T) *model.Storage {
	t.Helper()
	rowSeq++
	st := &model.Storage{
		Driver:    "PikPak",
		MountPath: fmt.Sprintf("/pikpak-test-%d", rowSeq),
		Addition:  `{"username":"tester@example.com","password":"pw"}`,
	}
	if err := db.CreateStorage(st); err != nil {
		t.Fatalf("failed to create storage row: %v", err)
	}
	return st
}

func persistedRefreshToken(t *testing.T, id uint) string {
	t.Helper()
	st, err := db.GetStorageById(id)
	if err != nil {
		t.Fatalf("failed to read storage back: %v", err)
	}
	var a Addition
	if err := json.Unmarshal([]byte(st.Addition), &a); err != nil {
		t.Fatalf("failed to decode persisted addition %q: %v", st.Addition, err)
	}
	return a.RefreshToken
}

// mockCall records one request received by the mock server.
type mockCall struct {
	headers http.Header
	body    map[string]any
}

func (c mockCall) captchaToken() string {
	s, _ := c.body["captcha_token"].(string)
	return s
}

// pikpakMock emulates the captcha/auth endpoints used by login() and
// refreshToken(), plus one drive endpoint that serves as the entry point of
// the recovery chain. The drive endpoint fails exactly once (with the code
// configured in driveFirstStatus) and succeeds afterwards, so request() can
// only complete if recovery actually ran.
type pikpakMock struct {
	t     *testing.T
	srv   *httptest.Server
	mu    sync.Mutex
	calls map[string][]mockCall

	captchaTokenOut string
	captchaURL      string

	tokenStatus int
	tokenBody   map[string]any

	signinStatus int
	signinBody   map[string]any

	driveFirstStatus int
	driveFirstBody   map[string]any // body served on the first drive call only
	driveBody        map[string]any // body served afterwards
	driveHits        int
}

func newPikpakMock(t *testing.T) *pikpakMock {
	t.Helper()
	m := &pikpakMock{
		t:                t,
		calls:            map[string][]mockCall{},
		captchaTokenOut:  "cap-fresh",
		tokenStatus:      http.StatusOK,
		tokenBody:        map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "sub": "user-1"},
		signinStatus:     http.StatusOK,
		signinBody:       map[string]any{"access_token": "at-new", "refresh_token": "rt-new", "sub": "user-1"},
		driveFirstStatus: http.StatusOK,
		driveFirstBody:   map[string]any{"files": []any{}, "next_page_token": ""},
		driveBody:        map[string]any{"files": []any{}, "next_page_token": ""},
	}
	m.srv = httptest.NewServer(http.HandlerFunc(m.serve))
	return m
}

func (m *pikpakMock) close() { m.srv.Close() }

func (m *pikpakMock) serve(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	if raw, err := io.ReadAll(r.Body); err == nil && len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	m.mu.Lock()
	m.calls[r.URL.Path] = append(m.calls[r.URL.Path], mockCall{headers: r.Header.Clone(), body: body})
	status := http.StatusOK
	payload := any(map[string]any{})
	switch {
	case strings.HasSuffix(r.URL.Path, "/v1/shield/captcha/init"):
		payload = map[string]any{"captcha_token": m.captchaTokenOut, "expires_in": 3600, "url": m.captchaURL}
	case strings.HasSuffix(r.URL.Path, "/v1/auth/signin"):
		status = m.signinStatus
		payload = m.signinBody
	case strings.HasSuffix(r.URL.Path, "/v1/auth/token"):
		status = m.tokenStatus
		payload = m.tokenBody
	case strings.HasSuffix(r.URL.Path, "/drive/v1/files"):
		m.driveHits++
		if m.driveHits == 1 {
			status = m.driveFirstStatus
			payload = m.driveFirstBody
		} else {
			payload = m.driveBody
		}
	default:
		m.mu.Unlock()
		m.t.Errorf("unexpected request to %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
		return
	}
	m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func (m *pikpakMock) count(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls[path])
}

func (m *pikpakMock) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = map[string][]mockCall{}
	m.driveHits = 0
}

func (m *pikpakMock) last(path string) mockCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	calls := m.calls[path]
	if len(calls) == 0 {
		m.t.Fatalf("no recorded call for %s", path)
	}
	return calls[len(calls)-1]
}

// installMockClient replaces base.RestyClient with a client whose requests to
// the hard-coded PikPak hosts are rewritten onto the mock server, and returns
// a restore function. The rewrite happens in OnBeforeRequest, which resty
// runs before its internal parseRequestURL/createHTTPRequest middlewares.
func installMockClient(m *pikpakMock) func() {
	old := base.RestyClient
	client := resty.New()
	client.OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		for _, host := range []string{"https://user.mypikpak.net", "https://api-drive.mypikpak.net"} {
			if strings.HasPrefix(req.URL, host) {
				req.URL = strings.Replace(req.URL, host, m.srv.URL, 1)
			}
		}
		return nil
	})
	base.RestyClient = client
	return func() { base.RestyClient = old }
}

// newTestDriver builds a PikPak with a fully initialized Common (web platform
// constants) and a fresh storage row in the DB, ready for auth-flow tests.
func newTestDriver(t *testing.T) (*PikPak, uint) {
	t.Helper()
	setupTestDB(t)
	st := createStorageRow(t)
	d := &PikPak{}
	d.SetStorage(*st)
	d.Platform = "web"
	d.Username = "tester@example.com"
	d.Password = "pw"
	d.Common = &Common{
		ClientID:      WebClientID,
		ClientSecret:  WebClientSecret,
		ClientVersion: WebClientVersion,
		PackageName:   WebPackageName,
		DeviceID:      "test-device",
		UserAgent:     "test-agent",
		Algorithms:    WebAlgorithms,
	}
	d.Common.RefreshCTokenCk = func(token string) {
		d.Common.CaptchaToken = token
	}
	return d, st.ID
}

const (
	pathCaptchaInit = "/v1/shield/captcha/init"
	pathSignin      = "/v1/auth/signin"
	pathToken       = "/v1/auth/token"
	pathFiles       = "/drive/v1/files"
)

// --- Main auth recovery path ---

// TestMainRecoveryPath exercises the full chain the PR is about: a drive
// request fails with 4122, refreshToken fails with 4126, login() runs (fresh
// captcha + password signin), the new refresh token is persisted to the DB,
// and request() retries the original call successfully with the new tokens.
func TestMainRecoveryPath(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, id := newTestDriver(t)
	d.RefreshToken = "rt-old"
	d.AccessToken = "at-stale"
	d.SetCaptchaToken("cap-stale")
	d.Addition.RefreshToken = "rt-old"

	// refresh attempt fails with "refresh token invalid"
	m.tokenStatus = http.StatusBadRequest
	m.tokenBody = map[string]any{"error_code": 4126, "error": "invalid_grant"}
	// the first drive call reports an expired access token; the retry succeeds
	m.driveFirstStatus = http.StatusBadRequest
	m.driveFirstBody = map[string]any{"error_code": 4122, "error": "access_token_expired"}
	m.driveBody = map[string]any{"files": []any{}, "next_page_token": ""}

	var resp Files
	if _, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodGet, nil, &resp); err != nil {
		t.Fatalf("request() returned error even though recovery should succeed: %v", err)
	}

	if got := m.count(pathToken); got != 1 {
		t.Errorf("expected exactly 1 refresh request, got %d", got)
	}
	if got := m.count(pathSignin); got != 1 {
		t.Errorf("expected exactly 1 signin (re-login), got %d", got)
	}
	if got := m.count(pathFiles); got != 2 {
		t.Errorf("expected 2 files requests (failed + retried), got %d", got)
	}
	if got := m.count(pathCaptchaInit); got != 1 {
		t.Errorf("expected exactly 1 captcha/init call during re-login, got %d", got)
	}

	// The retry must carry the tokens obtained via re-login, not the stale ones.
	lastFiles := m.last(pathFiles)
	if got := lastFiles.headers.Get("Authorization"); got != "Bearer at-new" {
		t.Errorf("retried request Authorization = %q, want %q", got, "Bearer at-new")
	}
	if got := lastFiles.headers.Get("X-Captcha-Token"); got != "cap-fresh" {
		t.Errorf("retried request X-Captcha-Token = %q, want %q", got, "cap-fresh")
	}

	// Tokens were rotated in memory...
	if d.AccessToken != "at-new" {
		t.Errorf("AccessToken = %q, want %q", d.AccessToken, "at-new")
	}
	if d.RefreshToken != "rt-new" {
		t.Errorf("RefreshToken = %q, want %q", d.RefreshToken, "rt-new")
	}
	// ...and the rotated refresh token was really persisted.
	if got := persistedRefreshToken(t, id); got != "rt-new" {
		t.Errorf("persisted addition refresh_token = %q, want %q", got, "rt-new")
	}
}

// TestRefreshToken4126WithoutCredentialsDoesNotLogin checks that a 4126 with
// empty username/password yields the "re-provide refresh_token" error instead
// of attempting a password login.
func TestRefreshToken4126WithoutCredentialsDoesNotLogin(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	d.Username = ""
	d.Password = ""

	m.tokenStatus = http.StatusBadRequest
	m.tokenBody = map[string]any{"error_code": 4126, "error": "invalid_grant"}

	err := d.refreshToken("rt-old")
	if err == nil {
		t.Fatal("refreshToken() with invalid refresh token and no credentials must fail")
	}
	if !strings.Contains(err.Error(), "re-provide") {
		t.Errorf("unexpected error text: %v", err)
	}
	if got := m.count(pathSignin); got != 0 {
		t.Errorf("signin must not be attempted without credentials, got %d calls", got)
	}
}

// TestRefreshTokenOtherErrorDoesNotLogin checks that a non-4126 refresh
// failure propagates without triggering a re-login (4126 is the single
// documented trigger).
func TestRefreshTokenOtherErrorDoesNotLogin(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	m.tokenStatus = http.StatusBadRequest
	m.tokenBody = map[string]any{"error_code": 10, "error_description": "too frequent"}

	if err := d.refreshToken("rt-old"); err == nil {
		t.Fatal("refreshToken() must propagate a non-4126 error")
	}
	if got := m.count(pathSignin); got != 0 {
		t.Errorf("signin must not be attempted for non-4126 errors, got %d calls", got)
	}
}

// --- Token validation (replaces TestTokenValidationRejectsEmpty) ---

// TestTokenValidationRejectsEmpty drives login() and refreshToken() against
// 200 responses that carry empty tokens and requires both paths to refuse
// them without persisting anything.
func TestTokenValidationRejectsEmpty(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	// login(): signin answers 200 but with an empty access_token.
	d, id := newTestDriver(t)
	m.signinBody = map[string]any{"access_token": "", "refresh_token": "rt-x", "sub": "user-1"}
	if err := d.login(); err == nil {
		t.Fatal("login() must reject empty access_token")
	}
	if got := persistedRefreshToken(t, id); got != "" {
		t.Errorf("login() must not persist tokens when validation fails, persisted %q", got)
	}

	// login(): symmetric case — empty refresh_token but non-empty access_token.
	d3, id3 := newTestDriver(t)
	m.reset()
	m.signinBody = map[string]any{"access_token": "at-x", "refresh_token": "", "sub": "user-1"}
	if err := d3.login(); err == nil {
		t.Fatal("login() must reject empty refresh_token")
	}
	if got := persistedRefreshToken(t, id3); got != "" {
		t.Errorf("login() must not persist tokens when validation fails, persisted %q", got)
	}

	// refreshToken(): 200 but empty refresh_token.
	d2, id2 := newTestDriver(t)
	m.tokenStatus = http.StatusOK
	m.tokenBody = map[string]any{"access_token": "at-x", "refresh_token": "", "sub": "user-1"}
	if err := d2.refreshToken("rt-old"); err == nil {
		t.Fatal("refreshToken() must reject empty refresh_token")
	}
	if got := persistedRefreshToken(t, id2); got != "" {
		t.Errorf("refreshToken() must not persist tokens when validation fails, persisted %q", got)
	}
}

// --- Captcha refresh (replaces TestCaptchaAlwaysRefreshedBeforeLogin) ---

// TestCaptchaAlwaysRefreshedBeforeLogin proves login() fetches a fresh captcha
// even when a (possibly expired) token is already present, and that signin is
// performed with the fresh token rather than the stale one.
func TestCaptchaAlwaysRefreshedBeforeLogin(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	d.SetCaptchaToken("cap-stale") // non-empty and (conceptually) expired

	if err := d.login(); err != nil {
		t.Fatalf("login() failed: %v", err)
	}

	if got := m.count(pathCaptchaInit); got != 1 {
		t.Fatalf("expected exactly 1 captcha/init call despite a non-empty stale token, got %d", got)
	}
	if got := m.last(pathSignin).captchaToken(); got != "cap-fresh" {
		t.Errorf("signin used captcha_token %q, want the fresh %q", got, "cap-fresh")
	}
	if got := d.GetCaptchaToken(); got != "cap-fresh" {
		t.Errorf("driver CaptchaToken = %q after login, want %q", got, "cap-fresh")
	}
}

// --- Stale bearer cleared before login ---

// TestLoginClearsStaleAccessToken checks that the captcha/init and signin
// requests issued by login() do not carry the expired bearer token.
func TestLoginClearsStaleAccessToken(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	d.AccessToken = "at-stale"

	if err := d.login(); err != nil {
		t.Fatalf("login() failed: %v", err)
	}

	for _, path := range []string{pathCaptchaInit, pathSignin} {
		if got := m.last(path).headers.Get("Authorization"); got != "" {
			t.Errorf("%s request carried Authorization %q, want it cleared before login", path, got)
		}
	}
}

// --- Captcha meta completeness ---

// TestCaptchaMetaCompleteness asserts captcha/init on the login path carries
// the same meta fields RefreshCaptchaTokenAtLogin sends on main.
func TestCaptchaMetaCompleteness(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	if err := d.login(); err != nil {
		t.Fatalf("login() failed: %v", err)
	}

	meta, _ := m.last(pathCaptchaInit).body["meta"].(map[string]any)
	for _, key := range []string{"email", "client_version", "package_name", "timestamp", "captcha_sign"} {
		if v, ok := meta[key]; !ok || v == "" {
			t.Errorf("captcha meta missing or empty %q (got %#v)", key, meta)
		}
	}
}

// --- refreshToken success path (highest-frequency production path) ---

// TestRefreshTokenSuccessRotatesAndPersists covers 4122 -> refreshToken()
// succeeding: rotated tokens land in memory, the retry carries the new bearer,
// and the new refresh token is persisted to the DB.
func TestRefreshTokenSuccessRotatesAndPersists(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, id := newTestDriver(t)
	d.RefreshToken = "rt-old"
	d.AccessToken = "at-stale"
	d.Addition.RefreshToken = "rt-old"

	m.tokenStatus = http.StatusOK
	m.tokenBody = map[string]any{"access_token": "at-2", "refresh_token": "rt-2", "sub": "user-1"}
	m.driveFirstStatus = http.StatusBadRequest
	m.driveFirstBody = map[string]any{"error_code": 4122, "error": "access_token_expired"}
	m.driveBody = map[string]any{"files": []any{}, "next_page_token": ""}

	var resp Files
	if _, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodGet, nil, &resp); err != nil {
		t.Fatalf("request() failed even though refresh should succeed: %v", err)
	}

	if got := m.count(pathSignin); got != 0 {
		t.Errorf("a successful refresh must not fall through to password login, got %d signin calls", got)
	}
	if d.AccessToken != "at-2" {
		t.Errorf("AccessToken = %q, want %q", d.AccessToken, "at-2")
	}
	if d.RefreshToken != "rt-2" {
		t.Errorf("RefreshToken = %q, want %q", d.RefreshToken, "rt-2")
	}
	if got := m.last(pathFiles).headers.Get("Authorization"); got != "Bearer at-2" {
		t.Errorf("retried request Authorization = %q, want %q", got, "Bearer at-2")
	}
	if got := persistedRefreshToken(t, id); got != "rt-2" {
		t.Errorf("persisted addition refresh_token = %q, want %q", got, "rt-2")
	}
}

// --- captcha expired (case 9) ---

// TestCaptchaExpiredRefreshesAndRetries covers request() case 9: a captcha
// error on a drive call triggers a captcha refresh and one retry.
func TestCaptchaExpiredRefreshesAndRetries(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	d, _ := newTestDriver(t)
	d.AccessToken = "at-ok"
	d.RefreshToken = "rt-ok"
	d.SetCaptchaToken("cap-stale")

	m.driveFirstStatus = http.StatusBadRequest
	m.driveFirstBody = map[string]any{"error_code": 9, "error": "captcha_invalid"}
	m.driveBody = map[string]any{"files": []any{}, "next_page_token": ""}

	var resp Files
	if _, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodGet, nil, &resp); err != nil {
		t.Fatalf("request() failed even though captcha refresh should recover: %v", err)
	}

	if got := m.count(pathCaptchaInit); got == 0 {
		t.Fatal("expected a captcha refresh after error code 9")
	}
	if got := m.count(pathFiles); got != 2 {
		t.Errorf("expected 2 files requests (failed + retried), got %d", got)
	}
	if got := m.count(pathSignin); got != 0 {
		t.Errorf("captcha recovery must not re-login, got %d signin calls", got)
	}
	if got := m.last(pathFiles).headers.Get("X-Captcha-Token"); got != "cap-fresh" {
		t.Errorf("retried request X-Captcha-Token = %q, want %q", got, "cap-fresh")
	}
}

// --- SkipVerification (added by this PR) ---

// TestSkipVerificationControlsVerificationURL covers the new config option:
// a captcha/init response carrying a human-verification url is fatal by
// default and ignored only when skip_verification is enabled.
func TestSkipVerificationControlsVerificationURL(t *testing.T) {
	m := newPikpakMock(t)
	defer m.close()
	restore := installMockClient(m)
	defer restore()

	m.captchaURL = "https://user.mypikpak.net/forbidden/test"

	d, _ := newTestDriver(t)
	if err := d.login(); err == nil {
		t.Fatal("login() must fail on a verification url by default")
	} else if !strings.Contains(err.Error(), "need verify") {
		t.Errorf("unexpected error: %v", err)
	}

	d2, _ := newTestDriver(t)
	d2.SkipVerification = true
	if err := d2.login(); err != nil {
		t.Fatalf("login() with skip_verification must ignore the url, got: %v", err)
	}
	if d2.AccessToken != "at-new" {
		t.Errorf("AccessToken = %q after skipped verification, want %q", d2.AccessToken, "at-new")
	}
}
