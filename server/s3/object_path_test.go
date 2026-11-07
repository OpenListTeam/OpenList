package s3

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCleanObjectKey(t *testing.T) {
	valid := []string{
		"",
		"file.txt",
		"a/b/c.txt",
		"archive.tar.gz",
		".hidden",
		"a//b",
		"..hidden",
		"a..b",
		"a/..b/c",
		"...",
		"dir/",
		"空格 与 中文/文件.txt",
	}
	for _, key := range valid {
		if err := cleanObjectKey(key); err != nil {
			t.Errorf("cleanObjectKey(%q) = %v, want nil", key, err)
		}
	}

	invalid := []string{
		".",
		"..",
		"a/..",
		"a/../b",
		"../b",
		"./b",
		"a/./b",
		"a/../..",
		"a\x00b",
	}
	for _, key := range invalid {
		if err := cleanObjectKey(key); err == nil {
			t.Errorf("cleanObjectKey(%q) = nil, want an error", key)
		}
	}
}

func TestObjectFSPath(t *testing.T) {
	tests := []struct {
		name       string
		bucketPath string
		objectName string
		want       string
		wantErr    bool
	}{
		{name: "plain key", bucketPath: "/data/bucket", objectName: "file.txt", want: "/data/bucket/file.txt"},
		{name: "nested key", bucketPath: "/data/bucket", objectName: "a/b/c.txt", want: "/data/bucket/a/b/c.txt"},
		// An empty segment is refused rather than collapsed: "a//b" and "a/b"
		// are two keys in S3, and collapsing them made a write to one overwrite
		// the other.
		{name: "empty segment is refused", bucketPath: "/data/bucket", objectName: "a//b", wantErr: true},
		// The point of the check: a key must never leave its bucket.
		{name: "parent segment", bucketPath: "/data/bucket", objectName: "../other/secret", wantErr: true},
		{name: "current segment", bucketPath: "/data/bucket", objectName: "./secret", wantErr: true},
		{name: "trailing parent", bucketPath: "/data/bucket", objectName: "a/../../etc/passwd", wantErr: true},
		// A bucket rooted at the virtual filesystem root is refused outright:
		// it would own every mount, so there is no "inside" to stay within.
		{name: "root bucket", bucketPath: "/", objectName: "a/b", wantErr: true},
		{name: "empty root bucket", bucketPath: "", objectName: "a/b", wantErr: true},
		{name: "root bucket parent", bucketPath: "/", objectName: "../a", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := objectFSPath(tc.bucketPath, tc.objectName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("objectFSPath(%q, %q) = %q, want an error", tc.bucketPath, tc.objectName, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("objectFSPath(%q, %q) = %v", tc.bucketPath, tc.objectName, err)
			}
			if got != tc.want {
				t.Fatalf("objectFSPath(%q, %q) = %q, want %q", tc.bucketPath, tc.objectName, got, tc.want)
			}
		})
	}
}

func TestObjectKeyGuardRejectsDotSegments(t *testing.T) {
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	})
	handler := objectKeyGuard(next)

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantCode   string
	}{
		{name: "plain object", target: "/bucket/file.txt", wantStatus: http.StatusOK},
		{name: "nested object", target: "/bucket/a/b.txt", wantStatus: http.StatusOK},
		{name: "list bucket", target: "/bucket", wantStatus: http.StatusOK},
		{name: "list with safe prefix", target: "/bucket?list-type=2&prefix=logs%2F", wantStatus: http.StatusOK},
		{name: "traversal in key", target: "/bucket/../other/secret", wantStatus: http.StatusBadRequest, wantCode: "InvalidArgument"},
		{name: "encoded traversal in key", target: "/bucket/%2e%2e/other/secret", wantStatus: http.StatusBadRequest, wantCode: "InvalidArgument"},
		{name: "traversal in prefix", target: "/bucket?list-type=2&prefix=../", wantStatus: http.StatusBadRequest, wantCode: "InvalidArgument"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached = false
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantCode != "" && !strings.Contains(rec.Body.String(), "<Code>"+tc.wantCode+"</Code>") {
				t.Fatalf("body does not carry %s: %s", tc.wantCode, rec.Body.String())
			}
			if wantReached := tc.wantStatus == http.StatusOK; reached != wantReached {
				t.Fatalf("downstream handler reached = %v, want %v", reached, wantReached)
			}
		})
	}
}

// TestObjectKeyGuardCoversCopySource makes sure the guard does not have to know
// about CopyObject: the source key is validated where it becomes a path, which
// objectFSPath covers.
func TestObjectKeyGuardCoversCopySource(t *testing.T) {
	for _, key := range []string{"../other/secret", "a/../../other/secret"} {
		if _, err := objectFSPath("/data/bucket", key); err == nil {
			t.Fatalf("objectFSPath accepted copy source %q", key)
		}
	}
}
