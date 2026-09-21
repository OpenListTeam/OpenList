package s3

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestObjectHashFromDriver(t *testing.T) {
	sum := md5.Sum([]byte("content"))
	obj := &model.Object{HashInfo: utils.NewHashInfo(utils.MD5, hex.EncodeToString(sum[:]))}
	hash, err := getObjectHash(context.Background(), "object", obj)
	if err != nil || hex.EncodeToString(hash) != hex.EncodeToString(sum[:]) {
		t.Fatalf("hash = %x, err = %v", hash, err)
	}
	for _, value := range []string{"invalid", "ab"} {
		obj.HashInfo = utils.NewHashInfo(utils.MD5, value)
		if _, err := getObjectHash(context.Background(), "object", obj); err == nil {
			t.Fatalf("accepted invalid MD5 %q", value)
		}
	}
}

func TestMultipartMetadataMatchesCurrentContent(t *testing.T) {
	b := &s3Backend{meta: new(sync.Map)}
	oldHash := md5.Sum([]byte("old"))
	newHash := md5.Sum([]byte("new"))
	b.meta.Store("object", objectMetadata{
		headers: map[string]string{"Content-Type": "text/plain"},
		hash:    oldHash[:],
		etag:    `"multipart-2"`,
	})
	meta, etag := b.loadMetadata("object", oldHash[:])
	if etag != `"multipart-2"` || meta["Content-Type"] != "text/plain" {
		t.Fatal("lost metadata for unchanged multipart content")
	}
	meta, etag = b.loadMetadata("object", newHash[:])
	if meta != nil || etag != "" {
		t.Fatal("reused a multipart validator after a same-size content change")
	}
}

func TestConditionalRequestsDoNotRedirect(t *testing.T) {
	for _, method := range []string{"GET", "PUT"} {
		for _, name := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range"} {
			for _, value := range []string{"*", ""} {
				r := httptest.NewRequest(method, "/bucket/object", nil)
				r.Header.Set(name, value)
				if url, ok := directObjectURL(r, nil); ok || url != "" {
					t.Fatalf("redirected %s with %s", method, name)
				}
				if url, ok := directUploadURL(r, nil); ok || url != "" {
					t.Fatalf("redirected %s with %s", method, name)
				}
			}
		}
	}
}
