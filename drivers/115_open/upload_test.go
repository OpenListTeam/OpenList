package _115_open

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

func TestRetryExpiredToken(t *testing.T) {
	expired := oss.ServiceError{StatusCode: 403, Code: "SecurityTokenExpired"}
	denied := oss.ServiceError{StatusCode: 403, Code: "AccessDenied"}
	refreshErr := errors.New("refresh failed")
	for _, tt := range []struct {
		name                    string
		first, next, refreshErr error
		calls, refreshes        int
		want                    error
	}{
		{name: "success", calls: 1},
		{name: "expired", first: expired, calls: 2, refreshes: 1},
		{name: "wrapped", first: fmt.Errorf("upload: %w", expired), calls: 2, refreshes: 1},
		{name: "access denied", first: denied, calls: 1, want: denied},
		{name: "refresh failure", first: expired, refreshErr: refreshErr, calls: 1, refreshes: 1, want: refreshErr},
		{name: "still expired", first: expired, next: expired, calls: 2, refreshes: 1, want: expired},
	} {
		t.Run(tt.name, func(t *testing.T) {
			old, fresh := &oss.Bucket{}, &oss.Bucket{}
			bucket := old
			calls, refreshes := 0, 0
			err := retryExpiredToken(func() error {
				refreshes++
				if tt.refreshErr != nil {
					return tt.refreshErr
				}
				bucket = fresh
				return nil
			}, func() error {
				calls++
				if calls == 1 {
					return tt.first
				}
				if bucket != fresh {
					t.Fatal("retry used old bucket")
				}
				return tt.next
			})
			if !errors.Is(err, tt.want) || calls != tt.calls || refreshes != tt.refreshes {
				t.Fatalf("error=%v calls=%d refreshes=%d", err, calls, refreshes)
			}
		})
	}
}

func TestRetryExpiredTokenMultipart(t *testing.T) {
	for _, stage := range []string{"initiate", "part", "complete"} {
		t.Run(stage, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				body, _ := io.ReadAll(r.Body)
				if stage != "initiate" && r.URL.Query().Get("uploadId") != "existing-upload" {
					t.Error("upload ID changed")
				}
				if stage == "part" && (string(body) != "file contents" || r.URL.Query().Get("partNumber") != "2") {
					t.Error("part content or number changed")
				}
				if stage == "complete" && (!strings.Contains(string(body), "part-etag") || r.Header.Get("x-oss-callback") != "callback") {
					t.Error("completion parts or callback changed")
				}
				if requests == 1 {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, `<Error><Code>SecurityTokenExpired</Code><Message>expired</Message></Error>`)
					return
				}
				if r.Header.Get("x-oss-security-token") != "fresh-token" || !strings.HasPrefix(r.Header.Get("Authorization"), "OSS fresh-key:") {
					t.Error("credentials were not replaced")
				}
				w.Header().Set("ETag", `"part-etag"`)
				if stage == "initiate" {
					fmt.Fprint(w, `<InitiateMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key><UploadId>existing-upload</UploadId></InitiateMultipartUploadResult>`)
				} else if stage == "complete" {
					fmt.Fprint(w, `<CompleteMultipartUploadResult><Bucket>bucket</Bucket><Key>object</Key></CompleteMultipartUploadResult>`)
				}
			}))
			defer srv.Close()
			newBucket := func(key, token string) *oss.Bucket {
				c, err := oss.New(srv.URL, key, "secret", oss.SecurityToken(token), oss.ForcePathStyle(true))
				if err != nil {
					t.Fatal(err)
				}
				b, err := c.Bucket("bucket")
				if err != nil {
					t.Fatal(err)
				}
				return b
			}
			bucket := newBucket("old-key", "old-token")
			imur := oss.InitiateMultipartUploadResult{Bucket: "bucket", Key: "object", UploadID: "existing-upload"}
			rd := strings.NewReader("file contents")
			err := retryExpiredToken(func() error {
				bucket = newBucket("fresh-key", "fresh-token")
				return nil
			}, func() error {
				switch stage {
				case "initiate":
					_, err := bucket.InitiateMultipartUpload("object")
					return err
				case "part":
					if _, err := rd.Seek(0, io.SeekStart); err != nil {
						return err
					}
					_, err := bucket.UploadPart(imur, rd, int64(rd.Len()), 2)
					return err
				default:
					_, err := bucket.CompleteMultipartUpload(imur, []oss.UploadPart{{PartNumber: 2, ETag: "part-etag"}}, oss.Callback("callback"))
					return err
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			if requests != 2 {
				t.Fatalf("got %d requests", requests)
			}
		})
	}
}
