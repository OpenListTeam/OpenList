package s3

import (
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestParseObjectPath(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		wantBucket string
		wantObject string
		wantOK     bool
	}{
		{name: "object", path: "/bucket/path/to/file.txt", wantBucket: "bucket", wantObject: "path/to/file.txt", wantOK: true},
		{name: "missing object", path: "/bucket", wantOK: false},
		{name: "empty bucket", path: "//file.txt", wantOK: false},
		{name: "empty object", path: "/bucket/", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, object, ok := parseObjectPath(tt.path)
			if ok != tt.wantOK || bucket != tt.wantBucket || object != tt.wantObject {
				t.Fatalf("parseObjectPath(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.path, bucket, object, ok, tt.wantBucket, tt.wantObject, tt.wantOK)
			}
		})
	}
}

func TestHasNonObjectQuery(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{name: "no query", raw: "/bucket/object", want: false},
		{name: "aws auth query", raw: "/bucket/object?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=abc", want: false},
		{name: "legacy auth query", raw: "/bucket/object?AWSAccessKeyId=ak&Signature=sig&Expires=1", want: false},
		{name: "sdk operation marker", raw: "/bucket/object?x-id=GetObject", want: false},
		{name: "response content disposition", raw: "/bucket/object?response-content-disposition=attachment%3Bfilename%3Dx", want: false},
		{name: "response content type", raw: "/bucket/object?response-content-type=text%2Fplain", want: false},
		// list-type is not a routing key in gofakes3: a path with an object
		// segment is dispatched to getObject regardless, so it must not block
		// a direct redirect.
		{name: "list query", raw: "/bucket/object?list-type=2", want: false},
		{name: "multipart uploads", raw: "/bucket/object?uploads", want: true},
		{name: "multipart upload id", raw: "/bucket/object?uploadId=123", want: true},
		{name: "versioning", raw: "/bucket/object?versioning", want: true},
		{name: "versions", raw: "/bucket/object?versions", want: true},
		{name: "bucket location", raw: "/bucket/object?location", want: true},
		{name: "version id", raw: "/bucket/object?versionId=abc", want: true},
		{name: "version id null", raw: "/bucket/object?versionId=null", want: false},
		{name: "version id empty", raw: "/bucket/object?versionId=", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.raw, nil)
			if got := hasNonObjectQuery(req); got != tt.want {
				t.Fatalf("hasNonObjectQuery(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestAsHTTPDirectUploadInfo(t *testing.T) {
	info := model.HttpDirectUploadInfo{UploadURL: "https://example.com/upload"}
	got, ok := asHTTPDirectUploadInfo(info)
	if !ok || got == nil || got.UploadURL != info.UploadURL {
		t.Fatalf("asHTTPDirectUploadInfo(value) = (%v, %v), want upload info", got, ok)
	}

	got, ok = asHTTPDirectUploadInfo(&info)
	if !ok || got == nil || got.UploadURL != info.UploadURL {
		t.Fatalf("asHTTPDirectUploadInfo(pointer) = (%v, %v), want upload info", got, ok)
	}

	got, ok = asHTTPDirectUploadInfo(struct{}{})
	if ok || got != nil {
		t.Fatalf("asHTTPDirectUploadInfo(unsupported) = (%v, %v), want nil false", got, ok)
	}
}
