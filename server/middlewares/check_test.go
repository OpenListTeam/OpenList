package middlewares

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/gin-gonic/gin"
)

func TestStoragesLoadedAdmitsRequestOrigin(t *testing.T) {
	originalMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	originalConf := conf.Conf
	originalLoaded := conf.StoragesLoaded
	t.Cleanup(func() {
		gin.SetMode(originalMode)
		conf.Conf = originalConf
		conf.StoragesLoaded = originalLoaded
	})
	conf.StoragesLoaded = true

	router := gin.New()
	router.Use(StoragesLoaded)
	router.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, conf.GetApiUrl(c.Request.Context()))
	})

	assertOrigin := func(name, siteURL, target string, header http.Header, want string) {
		t.Run(name, func(t *testing.T) {
			conf.Conf = &conf.Config{SiteURL: siteURL}

			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header = header
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if got := rec.Body.String(); got != want {
				t.Fatalf("origin = %q, want %q", got, want)
			}
		})
	}

	assertOrigin(
		"configured site URL",
		"https://openlist.example/base/",
		"http://ignored.example/",
		nil,
		"https://openlist.example/base",
	)
	assertOrigin(
		"forwarded request",
		"",
		"http://internal.example/",
		http.Header{
			"X-Forwarded-Proto": {"https"},
			"X-Forwarded-Host":  {"public.example"},
		},
		"https://public.example",
	)
}
