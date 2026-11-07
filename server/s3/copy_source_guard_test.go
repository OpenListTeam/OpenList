package s3

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEtagMatches(t *testing.T) {
	assert.True(t, etagMatches("*", ""))
	assert.True(t, etagMatches(`"abc"`, `"abc"`))
	assert.True(t, etagMatches("abc", `"abc"`))
	assert.True(t, etagMatches(`"zzz", "abc"`, `"abc"`))
	assert.False(t, etagMatches(`"zzz"`, `"abc"`))
	assert.False(t, etagMatches(`"abc"`, ""))
	assert.False(t, etagMatches(`W/"abc"`, `"abc"`))
}

func TestCopySourcePreconditionFailed(t *testing.T) {
	mod := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	earlier := mod.Add(-time.Hour).Format(http.TimeFormat)
	later := mod.Add(time.Hour).Format(http.TimeFormat)

	assert.True(t, copySourcePreconditionFailed(`"nope"`, "", "", "", `"abc"`, mod), "stale If-Match")
	assert.False(t, copySourcePreconditionFailed(`"abc"`, "", "", "", `"abc"`, mod), "matching If-Match")
	assert.True(t, copySourcePreconditionFailed("", `"abc"`, "", "", `"abc"`, mod), "matching If-None-Match")
	assert.False(t, copySourcePreconditionFailed("", `"nope"`, "", "", `"abc"`, mod), "other If-None-Match")
	assert.True(t, copySourcePreconditionFailed("", "", "", earlier, `"abc"`, mod), "source changed after If-Unmodified-Since")
	assert.False(t, copySourcePreconditionFailed("", "", "", later, `"abc"`, mod), "source older than If-Unmodified-Since")
	assert.True(t, copySourcePreconditionFailed("", "", later, "", `"abc"`, mod), "source not modified since")
	assert.False(t, copySourcePreconditionFailed("", "", earlier, "", `"abc"`, mod), "source modified since")
	// An ETag we cannot name must not silently satisfy a positive match.
	assert.True(t, copySourcePreconditionFailed(`"abc"`, "", "", "", "", mod), "unknown source ETag")
}

func TestCopySourceConditionGuardRefusesStaleSource(t *testing.T) {
	mod := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	identity := func(ctx context.Context, bucket, key string) (string, time.Time, bool) {
		if bucket == "src" && key == "a.txt" {
			return `"abc"`, mod, true
		}
		return "", time.Time{}, false
	}
	handler := copySourceConditionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, identity)

	send := func(ifMatch string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "http://s3/dst/a.txt", nil)
		req.Header.Set("X-Amz-Copy-Source", "/src/a.txt")
		if ifMatch != "" {
			req.Header.Set("X-Amz-Copy-Source-If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	assert.Equal(t, http.StatusOK, send(`"abc"`).Code, "matching precondition passes")
	assert.Equal(t, http.StatusPreconditionFailed, send(`"nope"`).Code, "stale precondition refused")
	assert.Equal(t, http.StatusOK, send("").Code, "no precondition passes")
}

func TestCopySourceConditionGuardOnUploadPartCopyHandler(t *testing.T) {
	mod := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	identity := func(ctx context.Context, bucket, key string) (string, time.Time, bool) {
		return `"abc"`, mod, true
	}
	handler := copySourceConditionGuard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), nil, identity)

	req := httptest.NewRequest(http.MethodPut, "http://s3/dst/a.txt?partNumber=1&uploadId=x", nil)
	req.Header.Set("X-Amz-Copy-Source", "/src/a.txt")
	req.Header.Set("X-Amz-Copy-Source-If-Match", `"nope"`)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code, "part copy is guarded too")
}
