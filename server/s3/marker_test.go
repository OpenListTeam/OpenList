package s3

import (
	"context"
	"io"
	"strings"
	"testing"

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
	if len(delimited.Contents) != 1 || delimited.Contents[0].Key != key || len(delimited.CommonPrefixes) != 0 {
		t.Fatalf("delimited listing = %#v, want marker content %q", delimited, key)
	}
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
