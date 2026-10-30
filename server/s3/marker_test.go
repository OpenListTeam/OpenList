package s3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/url_tree"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/gofakes3"
)

func TestS3MarkerLifecycle(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)
	key := "marker-" + sanitizeTestName(t.Name()) + "/"

	if _, err := b.PutObject(ctx, "mp", key, map[string]string{"Content-Type": "application/octet-stream"}, strings.NewReader(""), 0); err != nil {
		t.Fatalf("PutObject marker: %v", err)
	}

	head, err := b.HeadObject(ctx, "mp", key)
	if err != nil {
		t.Fatalf("HeadObject marker: %v", err)
	}
	if head.Size != 0 || head.Metadata["Last-Modified"] == "" {
		t.Fatalf("HeadObject marker = %#v, want size 0 and modification time", head)
	}
	if head.Metadata["Content-Type"] != "application/octet-stream" {
		t.Fatalf("HeadObject marker Content-Type = %q, want application/octet-stream", head.Metadata["Content-Type"])
	}
	if head.Metadata["Content-Disposition"] == "" {
		t.Fatal("HeadObject marker has no Content-Disposition")
	}

	object, err := b.GetObject(ctx, "mp", key, nil)
	if err != nil {
		t.Fatalf("GetObject marker: %v", err)
	}
	body, err := io.ReadAll(object.Contents)
	_ = object.Contents.Close()
	if err != nil || len(body) != 0 {
		t.Fatalf("GetObject marker body = %q, err = %v", body, err)
	}
	if object.Metadata["Content-Type"] != "application/octet-stream" || object.Metadata["Content-Disposition"] == "" {
		t.Fatalf("GetObject marker metadata = %#v, want default content metadata", object.Metadata)
	}

	list, err := b.ListBucket(ctx, "mp", nil, gofakes3.ListBucketPage{MaxKeys: 10})
	if err != nil {
		t.Fatalf("ListBucket marker: %v", err)
	}
	if len(list.Contents) != 1 || list.Contents[0].Key != key {
		t.Fatalf("ListBucket marker = %#v, want %q", list.Contents, key)
	}

	if _, err := b.PutObject(ctx, "mp", key, nil, strings.NewReader("not empty"), int64(len("not empty"))); s3ErrorCode(err) != gofakes3.ErrInvalidArgument {
		t.Fatalf("non-empty marker error = %v, want InvalidArgument", err)
	}
	if _, err := b.DeleteObject(ctx, "mp", key); err != nil {
		t.Fatalf("DeleteObject marker: %v", err)
	}
	if _, err := b.HeadObject(ctx, "mp", key); s3ErrorCode(err) != gofakes3.ErrNoSuchKey {
		t.Fatalf("HeadObject after delete error = %v, want NoSuchKey", err)
	}
	list, err = b.ListBucket(ctx, "mp", nil, gofakes3.ListBucketPage{MaxKeys: 10})
	if err != nil {
		t.Fatalf("ListBucket after delete: %v", err)
	}
	if len(list.Contents) != 0 {
		t.Fatalf("ListBucket after delete = %#v, want empty", list.Contents)
	}
}

func TestS3MarkerMultipartCompletion(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)
	key := "multipart-marker-" + sanitizeTestName(t.Name()) + "/"

	uploadID, err := b.CreateMultipartUpload(ctx, "mp", key, nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	etag, err := b.UploadPart(ctx, "mp", key, uploadID, 1, 0, strings.NewReader(""))
	if err != nil {
		t.Fatalf("UploadPart: %v", err)
	}
	if _, _, err = b.CompleteMultipartUpload(ctx, "mp", key, uploadID, &gofakes3.CompleteMultipartUploadRequest{
		Parts: []gofakes3.CompletedPart{{PartNumber: 1, ETag: etag}},
	}); err != nil {
		t.Fatalf("CompleteMultipartUpload: %v", err)
	}
	if _, err = b.HeadObject(ctx, "mp", key); err != nil {
		t.Fatalf("HeadObject after multipart completion: %v", err)
	}
}

func TestS3NestedMarkerListing(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)
	key := "nested/marker/"
	if _, err := b.PutObject(ctx, "mp", key, nil, strings.NewReader(""), 0); err != nil {
		t.Fatalf("PutObject marker: %v", err)
	}

	list, err := b.ListBucket(ctx, "mp", &gofakes3.Prefix{HasPrefix: true, Prefix: "nested/"}, gofakes3.ListBucketPage{MaxKeys: 10})
	if err != nil {
		t.Fatalf("recursive ListBucket: %v", err)
	}
	if len(list.Contents) != 1 || list.Contents[0].Key != key {
		t.Fatalf("recursive listing = %#v, want %q", list.Contents, key)
	}

	delimited, err := b.ListBucket(ctx, "mp", &gofakes3.Prefix{HasPrefix: true, Prefix: "nested/", HasDelimiter: true, Delimiter: "/"}, gofakes3.ListBucketPage{MaxKeys: 10})
	if err != nil {
		t.Fatalf("delimited ListBucket: %v", err)
	}
	if len(delimited.Contents) != 0 || len(delimited.CommonPrefixes) != 1 || delimited.CommonPrefixes[0].Prefix != key {
		t.Fatalf("delimited listing = %#v, want common prefix %q", delimited, key)
	}
}

func TestS3MarkerHTTPTrailingSlashLifecycle(t *testing.T) {
	ctx := context.Background()
	_, _ = setupMultipartBackend(t)
	handler, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	url := server.URL + "/mp/http-marker-" + sanitizeTestName(t.Name()) + "/"
	do := func(method string) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(""))
		if err != nil {
			t.Fatalf("new %s request: %v", method, err)
		}
		req.ContentLength = 0
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s marker: %v", method, err)
		}
		return response
	}

	put := do(http.MethodPut)
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT marker status = %d, want %d", put.StatusCode, http.StatusOK)
	}
	put.Body.Close()

	head := do(http.MethodHead)
	if head.StatusCode != http.StatusOK {
		t.Fatalf("HEAD marker status = %d, want %d", head.StatusCode, http.StatusOK)
	}
	head.Body.Close()

	get := do(http.MethodGet)
	body, readErr := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || readErr != nil || len(body) != 0 {
		t.Fatalf("GET marker status = %d, body = %q, err = %v", get.StatusCode, body, readErr)
	}

	deleteResponse := do(http.MethodDelete)
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE marker status = %d, want %d", deleteResponse.StatusCode, http.StatusNoContent)
	}
	deleteResponse.Body.Close()

	head = do(http.MethodHead)
	if head.StatusCode != http.StatusNotFound {
		t.Fatalf("HEAD deleted marker status = %d, want %d", head.StatusCode, http.StatusNotFound)
	}
	head.Body.Close()
}

func TestS3RootSlashMarkerListing(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)
	if _, err := b.PutObject(ctx, "mp", "/", nil, strings.NewReader(""), 0); err != nil {
		t.Fatalf("PutObject root marker: %v", err)
	}

	list, err := b.ListBucket(ctx, "mp", nil, gofakes3.ListBucketPage{MaxKeys: 10})
	if err != nil {
		t.Fatalf("ListBucket root marker: %v", err)
	}
	found := false
	for _, item := range list.Contents {
		if item.Key == "/" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListBucket root marker = %#v, want key /", list.Contents)
	}
}

func TestS3MarkerCopyETag(t *testing.T) {
	ctx := context.Background()
	b, _ := setupMultipartBackend(t)
	source := "copy-source/"
	destination := "copy-destination/"
	if _, err := b.PutObject(ctx, "mp", source, nil, strings.NewReader(""), 0); err != nil {
		t.Fatalf("PutObject source marker: %v", err)
	}
	result, err := b.CopyObject(ctx, "mp", source, "mp", destination, nil)
	if err != nil {
		t.Fatalf("CopyObject marker: %v", err)
	}
	if result.ETag != emptyObjectETag {
		t.Fatalf("CopyObject marker ETag = %q, want %q", result.ETag, emptyObjectETag)
	}
	if _, err := b.HeadObject(ctx, "mp", destination); err != nil {
		t.Fatalf("HeadObject copied marker: %v", err)
	}
}

func newMarkerHTTPServer(t *testing.T) *httptest.Server {
	t.Helper()
	handler, err := NewServer(context.Background())
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func doMarkerRequest(t *testing.T, client *http.Client, method, url, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new %s request: %v", method, err)
	}
	req.ContentLength = int64(len(body))
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func TestS3MarkerHTTPGetSkipsSameNameFileRedirect(t *testing.T) {
	ctx := context.Background()
	mount := "/" + sanitizeTestName(t.Name())
	addition, err := json.Marshal(map[string]any{
		"url_structure": "a:1:https://example.com/a",
	})
	if err != nil {
		t.Fatalf("marshal url tree addition: %v", err)
	}
	if _, err := op.CreateStorage(ctx, model.Storage{
		Driver:    "UrlTree",
		MountPath: mount,
		Addition:  string(addition),
	}); err != nil {
		t.Fatalf("create url tree storage: %+v", err)
	}
	if err := op.SaveSettingItem(&model.SettingItem{
		Key:   conf.S3Buckets,
		Value: `[{"name":"rd","path":"` + mount + `"}]`,
	}); err != nil {
		t.Fatalf("save s3 buckets setting: %+v", err)
	}

	server := newMarkerHTTPServer(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}

	file := doMarkerRequest(t, client, http.MethodGet, server.URL+"/rd/a", "")
	file.Body.Close()
	if file.StatusCode != http.StatusFound {
		t.Fatalf("GET file status = %d, want %d", file.StatusCode, http.StatusFound)
	}

	put := doMarkerRequest(t, client, http.MethodPut, server.URL+"/rd/a/", "")
	put.Body.Close()
	if put.StatusCode != http.StatusOK {
		t.Fatalf("PUT marker status = %d, want %d", put.StatusCode, http.StatusOK)
	}
	t.Cleanup(func() { _ = deleteS3Marker("rd", "a/") })

	get := doMarkerRequest(t, client, http.MethodGet, server.URL+"/rd/a/", "")
	body, readErr := io.ReadAll(get.Body)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || readErr != nil || len(body) != 0 {
		t.Fatalf("GET marker status = %d, location = %q, body = %q, err = %v",
			get.StatusCode, get.Header.Get("Location"), body, readErr)
	}
}

func TestS3MarkerHTTPMultipartKeepsTrailingSlash(t *testing.T) {
	_, localRoot := setupMultipartBackend(t)
	server := newMarkerHTTPServer(t)
	client := http.DefaultClient
	name := "multipart-http-" + sanitizeTestName(t.Name())
	url := server.URL + "/mp/" + name + "/"

	create := doMarkerRequest(t, client, http.MethodPost, url+"?uploads", "")
	var initiated gofakes3.InitiateMultipartUpload
	decodeErr := xml.NewDecoder(create.Body).Decode(&initiated)
	create.Body.Close()
	if create.StatusCode != http.StatusOK || decodeErr != nil {
		t.Fatalf("initiate status = %d, err = %v", create.StatusCode, decodeErr)
	}

	part := doMarkerRequest(t, client, http.MethodPut, url+"?partNumber=1&uploadId="+string(initiated.UploadID), "")
	part.Body.Close()
	if part.StatusCode != http.StatusOK {
		t.Fatalf("upload part status = %d, want %d", part.StatusCode, http.StatusOK)
	}

	complete := doMarkerRequest(t, client, http.MethodPost, url+"?uploadId="+string(initiated.UploadID),
		"<CompleteMultipartUpload><Part><PartNumber>1</PartNumber><ETag>"+part.Header.Get("ETag")+"</ETag></Part></CompleteMultipartUpload>")
	complete.Body.Close()
	if complete.StatusCode != http.StatusOK {
		t.Fatalf("complete status = %d, want %d", complete.StatusCode, http.StatusOK)
	}

	marker, err := getS3Marker("mp", name+"/")
	if err != nil || marker == nil {
		t.Fatalf("marker after multipart = %#v, err = %v", marker, err)
	}
	if _, err := os.Stat(filepath.Join(localRoot, name)); !os.IsNotExist(err) {
		t.Fatalf("multipart marker created a storage entry, stat err = %v", err)
	}
	head := doMarkerRequest(t, client, http.MethodHead, url, "")
	head.Body.Close()
	if head.StatusCode != http.StatusOK {
		t.Fatalf("HEAD marker status = %d, want %d", head.StatusCode, http.StatusOK)
	}
}
