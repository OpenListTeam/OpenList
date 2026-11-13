package google_drive

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type uploadRequestDoerFunc func(*http.Request) (*http.Response, error)

func (do uploadRequestDoerFunc) Do(req *http.Request) (*http.Response, error) {
	return do(req)
}

type signalReadCloser struct {
	io.Reader
	closed chan struct{}
}

func (body signalReadCloser) Close() error {
	close(body.closed)
	return nil
}

func TestDoUploadRequestWaitsForRequestBodyClose(t *testing.T) {
	requestBody := newUploadRequestBody(strings.NewReader("request"))
	responseClosed := make(chan struct{})
	releaseRequestBody := make(chan struct{})
	client := uploadRequestDoerFunc(func(req *http.Request) (*http.Response, error) {
		go func() {
			<-releaseRequestBody
			_ = req.Body.Close()
		}()
		return &http.Response{
			Body: signalReadCloser{Reader: strings.NewReader("response"), closed: responseClosed},
		}, nil
	})

	result := make(chan struct {
		body []byte
		err  error
	}, 1)
	go func() {
		body, err := doUploadRequest(client, &http.Request{Body: requestBody})
		result <- struct {
			body []byte
			err  error
		}{body, err}
	}()

	<-responseClosed
	select {
	case <-result:
		t.Fatal("upload returned before the HTTP request body was closed")
	case <-time.After(10 * time.Millisecond):
	}

	close(releaseRequestBody)
	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("doUploadRequest() error = %v", got.err)
		}
		if string(got.body) != "response" {
			t.Fatalf("doUploadRequest() body = %q, want %q", got.body, "response")
		}
	case <-time.After(time.Second):
		t.Fatal("doUploadRequest() did not finish after the request body closed")
	}
}
