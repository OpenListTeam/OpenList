package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

func TestGetMapsMissingPathToObjectNotFound(t *testing.T) {
	d, cleanup := newTestDriver(t, nil)
	defer cleanup()

	_, err := d.Get(context.Background(), "/missing")
	if !errs.IsObjectNotFound(err) {
		t.Fatalf("expected object not found, got %v", err)
	}
}

func TestMakeDirAfterMissingWebDAVStat(t *testing.T) {
	var mkcolCount atomic.Int32
	d, cleanup := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method == "MKCOL" && (r.URL.Path == "/new" || r.URL.Path == "/new/") {
			mkcolCount.Add(1)
			w.WriteHeader(http.StatusCreated)
			return true
		}
		return false
	})
	defer cleanup()

	if err := op.MakeDir(context.Background(), d, "/new"); err != nil {
		t.Fatalf("MakeDir failed: %v", err)
	}
	if got := mkcolCount.Load(); got != 1 {
		t.Fatalf("expected one MKCOL request, got %d", got)
	}
}

// TestGetRetriesCollectionWithTrailingSlash covers servers that return
// 200 HTML for a slash-less collection PROPFIND. RFC 4918 Section 5.2
// recommends using the trailing-slash for all collection URLs.
func TestGetRetriesCollectionWithTrailingSlash(t *testing.T) {
	collectionXML := func(href string) string {
		return `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>` + href + `</d:href>
    <d:propstat>
      <d:prop>
        <d:displayname>share</d:displayname>
        <d:resourcetype><d:collection/></d:resourcetype>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
	}
	d, cleanup := newTestDriver(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Method != "PROPFIND" {
			return false
		}
		switch r.URL.Path {
		case "/share":
			// The quirky server: no 207 without the trailing slash.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("<html>not a multistatus</html>"))
			return true
		case "/share/":
			w.Header().Set("Content-Type", "application/xml; charset=utf-8")
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = w.Write([]byte(collectionXML("/share/")))
			return true
		}
		return false
	})
	defer cleanup()

	obj, err := d.Get(context.Background(), "/share")
	if err != nil {
		t.Fatalf("Get on a collection whose slash-less PROPFIND 200s must succeed via the trailing-slash retry: %v", err)
	}
	if !obj.IsDir() {
		t.Fatalf("expected a folder, got file %q", obj.GetName())
	}
}

func newTestDriver(t *testing.T, extra func(http.ResponseWriter, *http.Request) bool) (*WebDav, func()) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if extra != nil && extra(w, r) {
			return
		}
		switch r.Method {
		case "PROPFIND":
			if r.URL.Path == "/" {
				w.Header().Set("Content-Type", "application/xml; charset=utf-8")
				w.WriteHeader(http.StatusMultiStatus)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/</d:href>
    <d:propstat>
      <d:prop>
        <d:displayname>/</d:displayname>
        <d:resourcetype><d:collection/></d:resourcetype>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
				return
			}
			http.NotFound(w, r)
		case "MKCOL":
			w.WriteHeader(http.StatusConflict)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	d := &WebDav{Addition: Addition{Address: srv.URL, RootPath: driver.RootPath{RootFolderPath: "/"}}}
	if err := d.Init(context.Background()); err != nil {
		srv.Close()
		t.Fatalf("init driver: %v", err)
	}
	return d, func() {
		_ = d.Drop(context.Background())
		srv.Close()
	}
}
