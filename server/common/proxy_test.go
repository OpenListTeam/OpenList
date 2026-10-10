package common

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	closed := 0
	link := &model.Link{
		URL: upstream.URL,
		SyncClosers: utils.NewSyncClosers(utils.CloseFunc(func() error {
			closed++
			return nil
		})),
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/sd/example", nil)

	err := Proxy(recorder, request, link, file, false)
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
	if closed != 1 {
		t.Errorf("link close count = %d, want 1", closed)
	}
}

func TestProxyRangePreservesRangeAndClosesLink(t *testing.T) {
	previousConfig := conf.Conf
	conf.Conf = conf.DefaultConfig("data")
	t.Cleanup(func() { conf.Conf = previousConfig })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "file.txt", time.Time{}, strings.NewReader("abcdef"))
	}))
	t.Cleanup(upstream.Close)

	for _, tc := range []struct {
		name, method, rangeHeader, body string
		status                          int
	}{
		{name: "single range", method: http.MethodGet, rangeHeader: "bytes=1-3", body: "bcd", status: http.StatusPartialContent},
		{name: "head", method: http.MethodHead, body: "", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := 0
			link := &model.Link{URL: upstream.URL, SyncClosers: utils.NewSyncClosers(utils.CloseFunc(func() error { closed++; return nil }))}
			request := httptest.NewRequest(tc.method, "/proxy/file.txt", nil)
			if tc.rangeHeader != "" {
				request.Header.Set("Range", tc.rangeHeader)
			}
			recorder := httptest.NewRecorder()
			if err := Proxy(recorder, request, link, &model.Object{Name: "file.txt", Size: 6}, true); err != nil {
				t.Fatal(err)
			}
			if recorder.Code != tc.status || recorder.Body.String() != tc.body || closed != 1 {
				t.Fatalf("status=%d body=%q closes=%d; want %d, %q, 1", recorder.Code, recorder.Body.String(), closed, tc.status, tc.body)
			}
		})
	}
}
