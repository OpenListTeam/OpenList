package handles

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/go-resty/resty/v2"
)

const (
	feishuMockTokenResponse    = `{"code":0,"access_token":"u-test-token","expires_in":7200,"token_type":"Bearer"}`
	feishuMockUserInfoResponse = `{"code":0,"msg":"success","data":{"open_id":"ou_test_openid","name":"张三","en_name":"San Zhang"}}`
)

// setFeishuSSOSettings keeps the SSO settings in the setting cache so the
// handlers do not need an initialised database.
func setFeishuSSOSettings(t *testing.T) {
	t.Helper()
	oldSecretKey := common.SecretKey
	common.SecretKey = []byte("feishu-test-secret")
	oldURL := conf.URL
	conf.URL = &url.URL{Path: "/"}
	settings := map[string]string{
		conf.SSOLoginEnabled:      "true",
		conf.SSOLoginPlatform:     "Feishu",
		conf.SSOClientId:          "cli_test",
		conf.SSOClientSecret:      "secret_test",
		conf.SSOCompatibilityMode: "false",
		conf.SSOPostMessageOrigin: "",
	}
	for key, value := range settings {
		op.Cache.SetSetting(key, &model.SettingItem{Key: key, Value: value})
	}
	t.Cleanup(func() {
		common.SecretKey = oldSecretKey
		conf.URL = oldURL
		op.Cache.ClearAll()
	})
}

func feishuTestContext(apiUrl, rawURL string, cookies []*http.Cookie) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(rec)
	// Matches server.Init, which is what lets GetApiUrl reach the value the
	// middleware stored on the request context.
	engine.ContextWithFallback = true
	req := httptest.NewRequest(http.MethodGet, rawURL, nil)
	if apiUrl != "" {
		req = req.WithContext(context.WithValue(req.Context(), conf.ApiUrlKey, apiUrl))
	}
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	c.Request = req
	return c, rec
}

type feishuRoundTripper struct {
	host string
}

func (rt feishuRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.URL.Scheme = "http"
	cloned.URL.Host = rt.host
	return http.DefaultTransport.RoundTrip(cloned)
}

// installFeishuMockClient replaces the shared resty client so requests to
// accounts.feishu.cn and open.feishu.cn are served by the test server.
func installFeishuMockClient(t *testing.T, serverURL string) {
	t.Helper()
	u, err := url.Parse(serverURL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	oldClient := ssoClient
	ssoClient = resty.New().SetRetryCount(0).SetTransport(feishuRoundTripper{host: u.Host})
	t.Cleanup(func() {
		ssoClient = oldClient
	})
}

func feishuTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/v3/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("token request method = %s, want POST", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			t.Errorf("token request content-type = %q, want form encoded", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse token request form: %v", err)
		}
		want := map[string]string{
			"grant_type":    "authorization_code",
			"client_id":     "cli_test",
			"client_secret": "secret_test",
			"code":          "feishu-code",
			"redirect_uri":  "https://openlist.example.com/api/auth/sso_callback?method=get_sso_id",
		}
		for key, value := range want {
			if got := r.PostForm.Get(key); got != value {
				t.Errorf("token form %s = %q, want %q", key, got, value)
			}
		}
		if _, ok := r.PostForm["scope"]; ok {
			t.Errorf("token form must not contain scope for Feishu")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, feishuMockTokenResponse)
	})
	mux.HandleFunc("/open-apis/authen/v1/user_info", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("user info request method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer u-test-token" {
			t.Errorf("user info Authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, feishuMockUserInfoResponse)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

var ssoIDPattern = regexp.MustCompile(`"sso_id":"([^"]+)"`)

func ssoIDFromResponse(t *testing.T, body string) string {
	t.Helper()
	match := ssoIDPattern.FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("no sso_id in response body: %s", body)
	}
	return match[1]
}

func TestFeishuAuthorizeRedirect(t *testing.T) {
	setFeishuSSOSettings(t)
	c, rec := feishuTestContext("https://openlist.example.com", "/api/auth/sso?method=sso_get_token", nil)

	SSOLoginRedirect(c)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	location, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	if location.Scheme != "https" || location.Host != "accounts.feishu.cn" || location.Path != "/open-apis/authen/v1/authorize" {
		t.Errorf("redirect target = %s, want Feishu authorize endpoint", location)
	}
	query := location.Query()
	if got := query.Get("client_id"); got != "cli_test" {
		t.Errorf("client_id = %q, want %q", got, "cli_test")
	}
	if got := query.Get("response_type"); got != "code" {
		t.Errorf("response_type = %q, want %q", got, "code")
	}
	if got := query.Get("redirect_uri"); got != "https://openlist.example.com/api/auth/sso_callback?method=sso_get_token" {
		t.Errorf("redirect_uri = %q", got)
	}
	if got := query.Get("state"); got != "" {
		t.Errorf("state = %q, want no state for a login request", got)
	}
}

func TestFeishuBindingCallback(t *testing.T) {
	setFeishuSSOSettings(t)
	server := feishuTestServer(t)
	installFeishuMockClient(t, server.URL)

	// Start the binding flow to obtain the signed state and the session cookie.
	c1, rec1 := feishuTestContext("https://openlist.example.com", "/api/auth/sso?method=get_sso_id", nil)
	SSOLoginRedirect(c1)
	if rec1.Code != http.StatusFound {
		t.Fatalf("redirect status = %d, want %d", rec1.Code, http.StatusFound)
	}
	location, err := url.Parse(rec1.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse redirect location: %v", err)
	}
	state := location.Query().Get("state")
	if state == "" {
		t.Fatal("binding redirect must carry a state")
	}
	if got := location.Query().Get("redirect_uri"); got != "https://openlist.example.com/api/auth/sso_callback?method=get_sso_id" {
		t.Errorf("redirect_uri = %q", got)
	}
	cookies := rec1.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("binding redirect must set the SSO binding cookie")
	}

	callbackURL := "/api/auth/sso_callback?method=get_sso_id&code=feishu-code&state=" + url.QueryEscape(state)
	c2, rec2 := feishuTestContext("https://openlist.example.com", callbackURL, cookies)
	SSOLoginCallback(c2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("callback status = %d, body = %s", rec2.Code, rec2.Body.String())
	}
	proof := ssoIDFromResponse(t, rec2.Body.String())
	claims, err := parseSSOBindingToken(c2, proof, ssoBindingProofPurpose)
	if err != nil {
		t.Fatalf("parse binding proof: %v", err)
	}
	if claims.SsoID != "ou_test_openid" {
		t.Errorf("sso id = %q, want the Feishu open_id", claims.SsoID)
	}
}

func TestFeishuCallbackRequiresCode(t *testing.T) {
	setFeishuSSOSettings(t)
	c, rec := feishuTestContext("https://openlist.example.com", "/api/auth/sso_callback?method=sso_get_token", nil)

	SSOLoginCallback(c)

	// The API reports errors in the JSON body with HTTP 200.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "No code provided") {
		t.Errorf("body = %s, want missing code error", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"code":400`) {
		t.Errorf("body = %s, want error code 400", rec.Body.String())
	}
}
