package handles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/gin-gonic/gin"
)

func ssoTestContext(apiUrl string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(rec)
	// Matches server.Init, which is what lets GetApiUrl reach the value the
	// middleware stored on the request context.
	engine.ContextWithFallback = true
	req := httptest.NewRequest(http.MethodGet, "/api/auth/sso?method=sso_get_token", nil)
	if apiUrl != "" {
		req = req.WithContext(context.WithValue(req.Context(), conf.ApiUrlKey, apiUrl))
	}
	c.Request = req
	// Keep setting lookups off the (uninitialised) database: ssoTargetOrigin
	// reads sso_postmessage_origin through the setting cache.
	op.Cache.SetSetting(conf.SSOPostMessageOrigin, &model.SettingItem{
		Key:   conf.SSOPostMessageOrigin,
		Value: "",
	})
	return c, rec
}

// A page that opens the SSO endpoint in a popup must not be able to read the
// token: the postMessage target origin has to name the site, never "*".
func TestSSOPostMessagePinsTargetOrigin(t *testing.T) {
	c, rec := ssoTestContext("https://openlist.example.com/base")
	ssoPostMessage(c, map[string]string{"token": "secret-token"})

	body := rec.Body.String()
	if strings.Contains(body, `"*"`) || strings.Contains(body, `, '*'`) {
		t.Fatalf("wildcard target origin present in response:\n%s", body)
	}
	if !strings.Contains(body, `"https://openlist.example.com"`) {
		t.Errorf("expected the site origin as target, got:\n%s", body)
	}
	if !strings.Contains(body, "secret-token") {
		t.Errorf("payload should still reach a legitimate opener, got:\n%s", body)
	}
}

// A frontend served from a different origin than the API needs the operator to
// be able to point the target at the frontend origin. The configured origin
// must win over the API origin.
func TestSSOPostMessageUsesConfiguredOrigin(t *testing.T) {
	c, rec := ssoTestContext("https://api.example.com/base")
	op.Cache.SetSetting(conf.SSOPostMessageOrigin, &model.SettingItem{
		Key:   conf.SSOPostMessageOrigin,
		Value: "https://frontend.example.com",
	})
	defer op.Cache.ClearAll()

	ssoPostMessage(c, map[string]string{"token": "secret-token"})

	body := rec.Body.String()
	if !strings.Contains(body, `"https://frontend.example.com"`) {
		t.Errorf("expected the configured origin as target, got:\n%s", body)
	}
}

// If the site URL cannot be resolved the fallback must tighten delivery to
// same-origin openers, not widen it back to every origin.
func TestSSOPostMessageFallsBackToSameOrigin(t *testing.T) {
	c, rec := ssoTestContext("")
	ssoPostMessage(c, map[string]string{"token": "secret-token"})

	body := rec.Body.String()
	if strings.Contains(body, `"*"`) {
		t.Fatalf("fallback must not be a wildcard origin:\n%s", body)
	}
	if !strings.Contains(body, `"/"`) {
		t.Errorf(`expected "/" fallback origin, got:\n%s`, body)
	}
}

// userID comes from the identity provider, so it must be encoded rather than
// interpolated into the JS string literal it used to land in.
func TestSSOPostMessageEscapesProviderControlledValue(t *testing.T) {
	c, rec := ssoTestContext("https://openlist.example.com")
	ssoPostMessage(c, map[string]string{"sso_id": `"});alert(document.domain);//`})

	body := rec.Body.String()
	if strings.Contains(body, `alert(document.domain)`) && !strings.Contains(body, `\"`) {
		t.Fatalf("provider value was not escaped:\n%s", body)
	}
	if !strings.Contains(body, `\"});alert`) {
		t.Errorf("expected the injected quote to be escaped, got:\n%s", body)
	}
}
