package net

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckPreconditions(t *testing.T) {
	modified := time.Date(2026, time.January, 2, 3, 4, 5, 123456789, time.UTC)
	date := modified.Format(http.TimeFormat)
	older := modified.Add(-time.Second).Format(http.TimeFormat)
	for _, tt := range []struct {
		name   string
		method string
		exists bool
		etag   string
		header http.Header
		status int
	}{
		{"unconditional create", "PUT", false, "", nil, 0},
		{"unconditional overwrite", "PUT", true, `"current"`, nil, 0},
		{"create only missing", "PUT", false, "", http.Header{"If-None-Match": {"*"}}, 0},
		{"create only existing", "PUT", true, `"current"`, http.Header{"If-None-Match": {"*"}}, 412},
		{"create only existing without etag", "PUT", true, "", http.Header{"If-None-Match": {"*"}}, 412},
		{"match missing", "PUT", false, "", http.Header{"If-Match": {"*"}}, 412},
		{"match existing", "PUT", true, `"current"`, http.Header{"If-Match": {"*"}}, 0},
		{"match existing without etag", "PUT", true, "", http.Header{"If-Match": {"*"}}, 0},
		{"match current", "PUT", true, `"current"`, http.Header{"If-Match": {`"current"`}}, 0},
		{"match stale", "PUT", true, `"current"`, http.Header{"If-Match": {`"stale"`}}, 412},
		{"match missing tag", "PUT", false, "", http.Header{"If-Match": {`"current"`}}, 412},
		{"match weak request", "PUT", true, `"current"`, http.Header{"If-Match": {`W/"current"`}}, 412},
		{"match weak representation", "PUT", true, `W/"current"`, http.Header{"If-Match": {`"current"`}}, 412},
		{"match list", "PUT", true, `"current"`, http.Header{"If-Match": {`"stale", "current"`}}, 0},
		{"match multiple lines", "PUT", true, `"current"`, http.Header{"If-Match": {`"stale"`, `"current"`}}, 0},
		{"match empty list members", "PUT", true, `"current"`, http.Header{"If-Match": {`, , "current",`}}, 0},
		{"match quoted comma", "PUT", true, `"one,two"`, http.Header{"If-Match": {`"stale", "one,two"`}}, 0},
		{"malformed match", "PUT", true, `"current"`, http.Header{"If-Match": {"current"}}, 412},
		{"empty match", "PUT", true, `"current"`, http.Header{"If-Match": {""}}, 412},
		{"none match current", "PUT", true, `"current"`, http.Header{"If-None-Match": {`"current"`}}, 412},
		{"none match stale", "PUT", true, `"current"`, http.Header{"If-None-Match": {`"stale"`}}, 0},
		{"none match missing tag", "PUT", false, "", http.Header{"If-None-Match": {`"current"`}}, 0},
		{"none match weak request", "PUT", true, `"current"`, http.Header{"If-None-Match": {`W/"current"`}}, 412},
		{"none match weak representation", "PUT", true, `W/"current"`, http.Header{"If-None-Match": {`"current"`}}, 412},
		{"none match multiple lines", "PUT", true, `"current"`, http.Header{"If-None-Match": {`"stale"`, `"current"`}}, 412},
		{"none match quoted comma", "PUT", true, `"one,two"`, http.Header{"If-None-Match": {`"stale", "one,two"`}}, 412},
		{"both conditions must pass", "PUT", true, `"current"`, http.Header{"If-Match": {`"current"`}, "If-None-Match": {`"current"`}}, 412},
		{"both conditions pass", "PUT", true, `"current"`, http.Header{"If-Match": {`"current"`}, "If-None-Match": {`"stale"`}}, 0},
		{"both wildcards missing", "PUT", false, "", http.Header{"If-Match": {"*"}, "If-None-Match": {"*"}}, 412},
		{"unmodified since older", "PUT", true, `"current"`, http.Header{"If-Unmodified-Since": {older}}, 412},
		{"unmodified since same second", "PUT", true, `"current"`, http.Header{"If-Unmodified-Since": {date}}, 0},
		{"unmodified since missing", "PUT", false, "", http.Header{"If-Unmodified-Since": {older}}, 0},
		{"invalid unmodified since", "PUT", true, `"current"`, http.Header{"If-Unmodified-Since": {"invalid"}}, 0},
		{"match overrides unmodified since", "PUT", true, `"current"`, http.Header{"If-Match": {`"current"`}, "If-Unmodified-Since": {older}}, 0},
		{"ignore modified since on put", "PUT", true, `"current"`, http.Header{"If-Modified-Since": {date}}, 0},
		{"get not modified", "GET", true, `"current"`, http.Header{"If-None-Match": {`W/"current"`}}, 304},
		{"head not modified", "HEAD", true, `"current"`, http.Header{"If-None-Match": {"*"}}, 304},
		{"get stale match", "GET", true, `"current"`, http.Header{"If-Match": {`"stale"`}}, 412},
		{"get modified since", "GET", true, `"current"`, http.Header{"If-Modified-Since": {date}}, 304},
		{"none match overrides modified since", "GET", true, `"current"`, http.Header{"If-None-Match": {`"stale"`}, "If-Modified-Since": {date}}, 0},
		{"empty none match overrides modified since", "GET", true, `"current"`, http.Header{"If-None-Match": {""}, "If-Modified-Since": {date}}, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, "/file.txt", nil)
			for name, values := range tt.header {
				for _, value := range values {
					r.Header.Add(name, value)
				}
			}
			w := httptest.NewRecorder()
			if tt.etag != "" {
				w.Header().Set("Etag", tt.etag)
			}
			var modTime time.Time
			if tt.exists {
				modTime = modified
			}
			done, _ := CheckPreconditions(w, r, modTime, tt.exists)
			if done != (tt.status != 0) {
				t.Fatalf("done = %v, want status %d", done, tt.status)
			}
			if done && w.Code != tt.status {
				t.Errorf("status = %d, want %d", w.Code, tt.status)
			}
			if w.Body.Len() != 0 {
				t.Errorf("unexpected response body: %q", w.Body.String())
			}
		})
	}
}

func TestCheckPreconditionsIfRange(t *testing.T) {
	for _, tt := range []struct {
		etag string
		want string
	}{
		{`"current"`, "bytes=0-3"},
		{`"stale"`, ""},
		{`W/"current"`, ""},
	} {
		t.Run(tt.etag, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/file.txt", nil)
			r.Header.Set("Range", "bytes=0-3")
			r.Header.Set("If-Range", tt.etag)
			w := httptest.NewRecorder()
			w.Header().Set("Etag", `"current"`)
			done, got := CheckPreconditions(w, r, time.Time{}, true)
			if done || got != tt.want {
				t.Fatalf("CheckPreconditions() = (%v, %q), want (false, %q)", done, got, tt.want)
			}
		})
	}
}
