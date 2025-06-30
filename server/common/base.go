package common

import (
	"context"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/internal/conf"
)

func GetApiUrlFormRequest(r *http.Request) string {
	api := conf.Conf.SiteURL
	if strings.HasPrefix(api, "http") {
		return strings.TrimSuffix(api, "/")
	}
	if r != nil {
		protocol := "http"
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			protocol = "https"
		}
		host := r.Header.Get("X-Forwarded-Host")
		if host == "" {
			host = r.Host
		}
		api = fmt.Sprintf("%s://%s", protocol, stdpath.Join(host, api))
	}
	api = strings.TrimSuffix(api, "/")
	return api
}

func GetApiUrl(ctx context.Context) string {
	val := ctx.Value(conf.ApiUrlKey)
	if api, ok := val.(string); ok {
		return api
	}
	return ""
}
