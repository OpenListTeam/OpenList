package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
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

func TestGetMultipartUploadPartSize(t *testing.T) {
	partSize, err := getMultipartUploadPartSize(defaultMultipartUploadPartSize*maxCopyParts, maxCopyParts, defaultMultipartUploadPartSize)
	if err != nil {
		t.Fatalf("getMultipartUploadPartSize: %v", err)
	}
	if partSize != defaultMultipartUploadPartSize {
		t.Fatalf("part size = %d, want %d", partSize, defaultMultipartUploadPartSize)
	}

	partSize, err = getMultipartUploadPartSize(defaultMultipartUploadPartSize*maxCopyParts+1, maxCopyParts, defaultMultipartUploadPartSize)
	if err != nil {
		t.Fatalf("getMultipartUploadPartSize: %v", err)
	}
	if partSize != defaultMultipartUploadPartSize+1 {
		t.Fatalf("grown part size = %d, want %d", partSize, defaultMultipartUploadPartSize+1)
	}

	partSize, err = getMultipartUploadPartSize(25*1024*1024, 2, 10*1024*1024)
	if err != nil {
		t.Fatalf("getMultipartUploadPartSize with custom max parts: %v", err)
	}
	if partSize != 25*1024*1024/2 {
		t.Fatalf("custom max parts size = %d, want %d", partSize, 25*1024*1024/2)
	}

	partSize, err = getMultipartUploadPartSize(25*1024*1024, maxCopyParts, 20*1024*1024)
	if err != nil {
		t.Fatalf("getMultipartUploadPartSize with custom chunk size: %v", err)
	}
	if partSize != 20*1024*1024 {
		t.Fatalf("custom chunk size = %d, want %d", partSize, 20*1024*1024)
	}

	partSize, err = getMultipartUploadPartSize(25*1024*1024, maxCopyParts, maxMultipartUploadPartSize+1)
	if err != nil {
		t.Fatalf("getMultipartUploadPartSize with oversized chunk size: %v", err)
	}
	if partSize != maxMultipartUploadPartSize {
		t.Fatalf("oversized chunk size = %d, want %d", partSize, maxMultipartUploadPartSize)
	}

	if _, err := getMultipartUploadPartSize(maxMultipartUploadPartSize*2+1, 2, defaultMultipartUploadPartSize); err == nil {
		t.Fatal("getMultipartUploadPartSize returned nil error for an oversized object")
	}
}

func TestGetDirectUploadInfoUsesMultipartForLargeFiles(t *testing.T) {
	const fileSize = 25 * 1024 * 1024
	created := false
	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !r.URL.Query().Has("uploads") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		created = true
		writeTestXML(t, w, `<InitiateMultipartUploadResult><UploadId>upload-id</UploadId></InitiateMultipartUploadResult>`)
	})
	d.EnableDirectUpload = true
	d.DirectUploadMaxParts = 2
	d.DirectUploadMinPartSize = 10 * 1024 * 1024

	info, err := d.GetDirectUploadInfo(context.Background(), "HttpDirect", &model.Object{Path: "/"}, "large-file", fileSize)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	multipartInfo, ok := info.(*model.S3MultipartDirectUploadInfo)
	if !ok {
		t.Fatalf("upload info type = %T, want S3MultipartDirectUploadInfo", info)
	}
	if multipartInfo.ChunkSize != fileSize/2 {
		t.Errorf("chunk size = %d, want %d", multipartInfo.ChunkSize, fileSize/2)
	}
	if len(multipartInfo.UploadURLs) != 2 {
		t.Fatalf("part URL count = %d, want 2", len(multipartInfo.UploadURLs))
	}
	if !created {
		t.Fatal("multipart upload was not initiated")
	}
}

func TestGetDirectUploadInfoUsesConfiguredDirectUploadMinPartSize(t *testing.T) {
	const fileSize = 25 * 1024 * 1024
	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !r.URL.Query().Has("uploads") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeTestXML(t, w, `<InitiateMultipartUploadResult><UploadId>upload-id</UploadId></InitiateMultipartUploadResult>`)
	})
	d.EnableDirectUpload = true
	d.DirectUploadMinPartSize = 20 * 1024 * 1024

	info, err := d.GetDirectUploadInfo(context.Background(), "HttpDirect", &model.Object{Path: "/"}, "large-file", fileSize)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	multipartInfo, ok := info.(*model.S3MultipartDirectUploadInfo)
	if !ok {
		t.Fatalf("upload info type = %T, want S3MultipartDirectUploadInfo", info)
	}
	if multipartInfo.ChunkSize != d.DirectUploadMinPartSize {
		t.Errorf("chunk size = %d, want %d", multipartInfo.ChunkSize, d.DirectUploadMinPartSize)
	}
	if len(multipartInfo.UploadURLs) != 2 {
		t.Fatalf("part URL count = %d, want 2", len(multipartInfo.UploadURLs))
	}
}

func TestGetDirectUploadInfoRejectsOversizedSinglePut(t *testing.T) {
	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		w.WriteHeader(http.StatusBadRequest)
	})
	d.EnableDirectUpload = true
	d.DirectUploadMaxParts = 1

	if _, err := d.GetDirectUploadInfo(context.Background(), "HttpDirect", &model.Object{Path: "/"}, "large-file", maxMultipartUploadPartSize+1); err == nil {
		t.Fatal("GetDirectUploadInfo returned nil error for oversized single PUT")
	}
}

func TestGetDirectUploadInfoUsesSinglePutWhenMultipartMaxPartsIsOne(t *testing.T) {
	const fileSize = 25 * 1024 * 1024
	putRequests := 0
	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Query().Get("uploadId") != "" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		putRequests++
		w.WriteHeader(http.StatusOK)
	})
	d.EnableDirectUpload = true
	d.DirectUploadMaxParts = 1

	info, err := d.GetDirectUploadInfo(context.Background(), "HttpDirect", &model.Object{Path: "/"}, "large-file", fileSize)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	httpInfo, ok := info.(*model.HttpDirectUploadInfo)
	if !ok {
		t.Fatalf("upload info type = %T, want HttpDirectUploadInfo", info)
	}
	if httpInfo.UploadURL == "" {
		t.Fatal("single upload URL is empty")
	}
	if putRequests != 0 {
		t.Fatalf("PutObject was executed while presigning, requests = %d", putRequests)
	}
}

func TestDirectMultipartUploadCompletesWithUploadedPartETags(t *testing.T) {
	const fileSize = 25 * 1024 * 1024
	uploadedParts := make(map[string]string)
	completed := false
	aborted := false
	d := newTestS3Driver(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Has("uploads"):
			writeTestXML(t, w, `<InitiateMultipartUploadResult><UploadId>upload-id</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && r.URL.Query().Get("uploadId") == "upload-id":
			partNumber := r.URL.Query().Get("partNumber")
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read part %s body: %v", partNumber, err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			uploadedParts[partNumber] = string(body)
			w.Header().Set("ETag", fmt.Sprintf(`"etag-%s"`, partNumber))
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Query().Get("uploadId") == "upload-id":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Errorf("read completion body: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			for partNumber := 1; partNumber <= 3; partNumber++ {
				want := fmt.Sprintf("<PartNumber>%d</PartNumber><ETag>\"etag-%d\"</ETag>", partNumber, partNumber)
				if !strings.Contains(string(body), want) {
					t.Errorf("completion body does not contain %q: %s", want, body)
				}
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
	d.EnableDirectUpload = true

	info, err := d.GetDirectUploadInfo(context.Background(), "HttpDirect", &model.Object{Path: "/"}, "large-file", fileSize)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	multipartInfo, ok := info.(*model.S3MultipartDirectUploadInfo)
	if !ok {
		t.Fatalf("upload info type = %T, want S3MultipartDirectUploadInfo", info)
	}
	for i, uploadURL := range multipartInfo.UploadURLs {
		request, err := http.NewRequestWithContext(context.Background(), http.MethodPut, uploadURL, bytes.NewBufferString(fmt.Sprintf("part-%d", i+1)))
		if err != nil {
			t.Fatalf("create part %d request: %v", i+1, err)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatalf("upload part %d: %v", i+1, err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("part %d status = %d, want %d", i+1, response.StatusCode, http.StatusOK)
		}
		if got := response.Header.Get("ETag"); got != fmt.Sprintf(`"etag-%d"`, i+1) {
			t.Fatalf("part %d ETag = %q", i+1, got)
		}
	}

	completion := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"etag-1"</ETag></Part><Part><PartNumber>2</PartNumber><ETag>"etag-2"</ETag></Part><Part><PartNumber>3</PartNumber><ETag>"etag-3"</ETag></Part></CompleteMultipartUpload>`
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, multipartInfo.CompleteURL, strings.NewReader(completion))
	if err != nil {
		t.Fatalf("create completion request: %v", err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("complete multipart upload: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("completion status = %d, want %d", response.StatusCode, http.StatusOK)
	}
	if !completed {
		t.Fatal("multipart upload was not completed")
	}
	if aborted {
		t.Fatal("successful multipart upload was aborted")
	}
	if len(uploadedParts) != len(multipartInfo.UploadURLs) {
		t.Fatalf("uploaded parts = %d, want %d", len(uploadedParts), len(multipartInfo.UploadURLs))
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
		Addition:           Addition{Bucket: "bucket", SignURLExpire: 4},
		client:             awss3.New(sess),
		directUploadClient: awss3.New(sess),
	}
}

func writeTestXML(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/xml")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}
