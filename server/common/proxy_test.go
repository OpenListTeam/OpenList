package common

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestProxyOverridesUpstreamContentDisposition(t *testing.T) {
	previousConfig := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() {
		conf.Conf = previousConfig
	})

	const content = "archive content"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="download"`)
		w.Header().Set("Content-Type", "application/x-rar-compressed")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, content)
	}))
	t.Cleanup(upstream.Close)

	file := &model.Object{
		Name: "测试文件.rar",
		Size: int64(len(content)),
	}
	link := &model.Link{URL: upstream.URL}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/sd/example", nil)

	err := Proxy(recorder, request, link, file)
	if err != nil {
		t.Fatalf("Proxy() error = %v", err)
	}
	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Fatalf("status code = %d, want %d", got, want)
	}
	if got, want := recorder.Header().Get("Content-Disposition"), utils.GenerateContentDisposition(file.GetName()); got != want {
		t.Errorf("Content-Disposition = %q, want %q", got, want)
	}
	if got, want := recorder.Header().Get("Content-Type"), "application/x-rar-compressed"; got != want {
		t.Errorf("Content-Type = %q, want %q", got, want)
	}
	if got, want := recorder.Body.String(), content; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}
