package s3

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	awss3 "github.com/aws/aws-sdk-go/service/s3"
)

func TestCopyFileUsesCopyObjectAtLimit(t *testing.T) {
	copyRequests := 0
	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Query().Get("uploadId") != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		copyRequests++
		writeTestXML(t, w, `<CopyObjectResult><ETag>"copy"</ETag></CopyObjectResult>`)
	})

	if err := d.copyFile(context.Background(), "source+file", "destination", maxCopyObjectSize); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if copyRequests != 1 {
		t.Fatalf("copy requests = %d, want 1", copyRequests)
	}
}

func TestCopyFileUsesMultipartCopyAboveLimit(t *testing.T) {
	size := maxCopyObjectSize + 1
	wantParts := int((size + defaultCopyPartSize - 1) / defaultCopyPartSize)
	ranges := make(map[int]string, wantParts)
	completed := false
	aborted := false

	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Cache-Control", "max-age=60")
			w.Header().Set("Content-Disposition", "attachment")
			w.Header().Set("Expires", "Wed, 21 Oct 2015 07:28:00 GMT")
			w.Header().Set("X-Amz-Meta-Source", "preserved")
			w.Header().Set("X-Amz-Website-Redirect-Location", "/redirect")
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			if got := r.Header.Get("Cache-Control"); got != "max-age=60" {
				t.Errorf("Cache-Control = %q, want %q", got, "max-age=60")
			}
			if got := r.Header.Get("Content-Disposition"); got != "attachment" {
				t.Errorf("Content-Disposition = %q, want %q", got, "attachment")
			}
			if got := r.Header.Get("Content-Type"); got != "application/octet-stream" {
				t.Errorf("Content-Type = %q, want %q", got, "application/octet-stream")
			}
			if got := r.Header.Get("Expires"); got != "Wed, 21 Oct 2015 07:28:00 GMT" {
				t.Errorf("Expires = %q, want an unchanged HTTP date", got)
			}
			if got := r.Header.Get("X-Amz-Meta-Source"); got != "preserved" {
				t.Errorf("metadata = %q, want %q", got, "preserved")
			}
			if got := r.Header.Get("X-Amz-Website-Redirect-Location"); got != "/redirect" {
				t.Errorf("website redirect = %q, want %q", got, "/redirect")
			}
			writeTestXML(t, w, `<InitiateMultipartUploadResult><UploadId>upload-id</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && r.URL.Query().Get("uploadId") == "upload-id":
			partNumber, err := strconv.Atoi(r.URL.Query().Get("partNumber"))
			if err != nil {
				t.Errorf("invalid part number: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if got := r.Header.Get("X-Amz-Copy-Source"); !strings.Contains(got, "source%2Bfile") {
				t.Errorf("copy source = %q, want encoded source key", got)
			}
			ranges[partNumber] = r.Header.Get("X-Amz-Copy-Source-Range")
			writeTestXML(t, w, fmt.Sprintf(`<CopyPartResult><ETag>"part-%d"</ETag></CopyPartResult>`, partNumber))
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "upload-id":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read complete body: %v", err)
			}
			if got := strings.Count(string(body), "<Part>"); got != wantParts {
				t.Errorf("completed parts = %d, want %d", got, wantParts)
			}
			completed = true
			writeTestXML(t, w, `<CompleteMultipartUploadResult><ETag>"complete"</ETag></CompleteMultipartUploadResult>`)
		case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") == "upload-id":
			aborted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	if err := d.copyFile(context.Background(), "source+file", "destination", size); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	if !completed {
		t.Fatal("multipart upload was not completed")
	}
	if aborted {
		t.Fatal("successful multipart upload was aborted")
	}
	if len(ranges) != wantParts {
		t.Fatalf("copied parts = %d, want %d", len(ranges), wantParts)
	}
	if got := ranges[1]; got != fmt.Sprintf("bytes=0-%d", defaultCopyPartSize-1) {
		t.Errorf("first range = %q", got)
	}
	lastStart := int64(wantParts-1) * defaultCopyPartSize
	if got := ranges[wantParts]; got != fmt.Sprintf("bytes=%d-%d", lastStart, size-1) {
		t.Errorf("last range = %q", got)
	}
}

func TestCopyFileMultipartAbortsOnPartFailure(t *testing.T) {
	size := maxCopyObjectSize + 1
	aborted := false
	completed := false

	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			writeTestXML(t, w, `<InitiateMultipartUploadResult><UploadId>upload-id</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && r.URL.Query().Get("uploadId") == "upload-id":
			w.WriteHeader(http.StatusInternalServerError)
			writeTestXML(t, w, `<Error><Code>InternalError</Code><Message>copy failed</Message></Error>`)
		case r.Method == http.MethodDelete && r.URL.Query().Get("uploadId") == "upload-id":
			aborted = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "upload-id":
			completed = true
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
		}
	})

	if err := d.copyFile(context.Background(), "source", "destination", size); err == nil {
		t.Fatal("copyFile returned nil error")
	}
	if !aborted {
		t.Fatal("failed multipart upload was not aborted")
	}
	if completed {
		t.Fatal("failed multipart upload was completed")
	}
}

func TestGetCopyPartSize(t *testing.T) {
	partSize, err := getCopyPartSize(defaultCopyPartSize * maxCopyParts)
	if err != nil {
		t.Fatalf("getCopyPartSize: %v", err)
	}
	if partSize != defaultCopyPartSize {
		t.Fatalf("part size = %d, want %d", partSize, defaultCopyPartSize)
	}

	partSize, err = getCopyPartSize(defaultCopyPartSize*maxCopyParts + 1)
	if err != nil {
		t.Fatalf("getCopyPartSize: %v", err)
	}
	if partSize != defaultCopyPartSize+1 {
		t.Fatalf("grown part size = %d, want %d", partSize, defaultCopyPartSize+1)
	}

	if _, err := getCopyPartSize(maxCopyPartSize*maxCopyParts + 1); err == nil {
		t.Fatal("getCopyPartSize returned nil error for an oversized object")
	}
}

func newTestS3Driver(t *testing.T, handler http.HandlerFunc) *S3 {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	sess, err := session.NewSession(&aws.Config{
		Credentials:      credentials.NewStaticCredentials("access-key", "secret-key", ""),
		Endpoint:         aws.String(server.URL),
		Region:           aws.String("us-east-1"),
		S3ForcePathStyle: aws.Bool(true),
		MaxRetries:       aws.Int(0),
	})
	if err != nil {
		t.Fatalf("create AWS session: %v", err)
	}
	return &S3{
		Addition: Addition{Bucket: "bucket"},
		client:   awss3.New(sess),
	}
}

func writeTestXML(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/xml")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}
