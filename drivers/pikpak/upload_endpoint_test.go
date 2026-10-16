package pikpak

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aliyun/aliyun-oss-go-sdk/oss"
)

func TestNormalizeUploadEndpoint(t *testing.T) {
	const bucket = "vip-lixian-07"
	for _, endpoint := range []string{
		"mypikpak.com",
		"mypikpak.net",
		"upload-a10b.mypikpak.com",
		"upload-a10b.mypikpak.net",
		"vip-lixian-07.mypikpak.com",
		"vip-lixian-07.mypikpak.net",
		"vip-lixian-07.upload-a10b.mypikpak.com",
		"https://upload-a10b.mypikpak.com",
		"http://vip-lixian-07.mypikpak.net",
		"https://mypikpak.com/",
		"UPLOAD-A10B.MYPIKPAK.COM.",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if got := normalizeUploadEndpoint(endpoint, bucket); got != "https://mypikpak.com" {
				t.Fatalf("normalizeUploadEndpoint(%q, %q) = %q", endpoint, bucket, got)
			}
		})
	}
}

func TestNormalizeUploadEndpointPreservesOtherEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"",
		"localhost",
		"net",
		"https://%",
		"oss-ap-southeast-1.aliyuncs.com",
		"https://storage.example.com",
		"mypikpak.com.example.com",
		"notmypikpak.com",
		"upload-a10b.mypikpak.com.example.com",
		"another-region.mypikpak.com",
		"other-bucket.mypikpak.com",
		"upload-.mypikpak.com",
		"ftp://upload-a10b.mypikpak.com",
		"https://user@upload-a10b.mypikpak.com",
		"https://upload-a10b.mypikpak.com:8443",
		"https://upload-a10b.mypikpak.com/path",
		"https://upload-a10b.mypikpak.com?option=value",
		"https://upload-a10b.mypikpak.com#fragment",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if got := normalizeUploadEndpoint(endpoint, "vip-lixian-07"); got != endpoint {
				t.Fatalf("unexpected rewrite: %q -> %q", endpoint, got)
			}
		})
	}
	if got := normalizeUploadEndpoint("vip-lixian-07.mypikpak.com", ""); got != "vip-lixian-07.mypikpak.com" {
		t.Fatalf("rewrote a bucket hostname without a matching bucket: %q", got)
	}
}

type uploadRoundTripFunc func(*http.Request) (*http.Response, error)

func (f uploadRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Use the real OSS SDK to check both upload methods. String normalization alone
// would not detect the SDK adding the bucket prefix a second time.
func TestUploadEndpointWithOSSSDK(t *testing.T) {
	for _, bucketName := range []string{"vip-lixian-07", "vip-lixian-12"} {
		for _, endpoint := range []string{
			"mypikpak.com", "mypikpak.net", "upload-a10b.mypikpak.com",
			bucketName + ".mypikpak.net", bucketName + ".upload-a10b.mypikpak.com",
		} {
			t.Run(bucketName+"/"+endpoint, func(t *testing.T) {
				var methods []string
				client, err := oss.New(normalizeUploadEndpoint(endpoint, bucketName), "test-key", "test-secret",
					oss.HTTPClient(&http.Client{Transport: uploadRoundTripFunc(func(r *http.Request) (*http.Response, error) {
						methods = append(methods, r.Method)
						if r.URL.Host != bucketName+".mypikpak.com" || r.URL.Scheme != "https" {
							t.Fatalf("unexpected upload destination: %s://%s", r.URL.Scheme, r.URL.Host)
						}
						if r.URL.Path != "/upload_tmp/test" {
							t.Fatalf("unexpected object path: %q", r.URL.Path)
						}
						body := ""
						if r.Method == http.MethodPost {
							if !r.URL.Query().Has("uploads") {
								t.Fatalf("expected multipart initiation, got query %q", r.URL.RawQuery)
							}
							body = "<InitiateMultipartUploadResult><Bucket>" + bucketName + "</Bucket><Key>upload_tmp/test</Key><UploadId>test-id</UploadId></InitiateMultipartUploadResult>"
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
					})}))
				if err != nil {
					t.Fatal(err)
				}
				bucket, err := client.Bucket(bucketName)
				if err != nil {
					t.Fatal(err)
				}
				if err := bucket.PutObject("upload_tmp/test", strings.NewReader("test payload")); err != nil {
					t.Fatal(err)
				}
				if _, err := bucket.InitiateMultipartUpload("upload_tmp/test"); err != nil {
					t.Fatal(err)
				}
				if got := strings.Join(methods, ","); got != "PUT,POST" {
					t.Fatalf("expected both upload paths, got %q", got)
				}
			})
		}
	}
}
