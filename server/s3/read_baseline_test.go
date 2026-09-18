package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/gofakes3"
	"gorm.io/gorm"
)

type baselineLocalCounts struct {
	get       atomic.Int64
	link      atomic.Int64
	readClose atomic.Int64
}

type baselineLocalDriver struct {
	local.Local
	counts *baselineLocalCounts
}

func (d *baselineLocalDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = "Stage0CountedLocal"
	return c
}

func (d *baselineLocalDriver) Get(ctx context.Context, path string) (model.Obj, error) {
	d.counts.get.Add(1)
	return d.Local.Get(ctx, path)
}

func (d *baselineLocalDriver) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	d.counts.link.Add(1)
	if file.GetName() == "warm.txt" {
		data := []byte("warm contents")
		ttl := time.Minute
		return &model.Link{
			ContentLength: int64(len(data)),
			Expiration:    &ttl,
			RangeReader: stream.RangeReaderFunc(func(ctx context.Context, requested http_range.Range) (io.ReadCloser, error) {
				end := int64(len(data))
				if requested.Length >= 0 && requested.Start+requested.Length < end {
					end = requested.Start + requested.Length
				}
				return utils.NewReadCloser(bytes.NewReader(data[requested.Start:end]), func() error {
					d.counts.readClose.Add(1)
					return nil
				}), nil
			}),
		}, nil
	}
	return d.Local.Link(ctx, file, args)
}

func TestGetObjectBaselineLookup(t *testing.T) {
	ctx := context.Background()
	counts := &baselineLocalCounts{}
	op.RegisterDriver(func() driver.Driver { return &baselineLocalDriver{counts: counts} })
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("baseline contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "warm.txt"), []byte("warm contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	addition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	mount := "/" + sanitizeTestName(t.Name())
	id, err := op.CreateStorage(ctx, model.Storage{Driver: "Stage0CountedLocal", MountPath: mount, Addition: string(addition)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(ctx, id); err != nil {
			t.Errorf("delete fixture storage: %v", err)
		}
	})
	previousBuckets, previousBucketsErr := op.GetSettingItemByKey(conf.S3Buckets)
	if previousBucketsErr != nil && !errors.Is(previousBucketsErr, gorm.ErrRecordNotFound) {
		t.Fatal(previousBucketsErr)
	}
	if err := op.SaveSettingItem(&model.SettingItem{Key: conf.S3Buckets, Value: `[{"name":"baseline","path":"` + mount + `"}]`}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if previousBucketsErr == nil {
			if err := op.SaveSettingItem(previousBuckets); err != nil {
				t.Errorf("restore S3 buckets: %v", err)
			}
			return
		}
		if err := db.DeleteSettingItemByKey(conf.S3Buckets); err != nil {
			t.Errorf("delete fixture S3 buckets: %v", err)
		}
		op.SettingCacheUpdate()
	})
	backend := newBackend().(*s3Backend)
	for i, want := range []string{"baseline contents", "baseline contents"} {
		obj, err := backend.GetObject(ctx, "baseline", "fixture.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(obj.Contents)
		if err != nil {
			t.Fatal(err)
		}
		if err := obj.Contents.Close(); err != nil {
			t.Fatal(err)
		}
		if string(body) != want || obj.Size != int64(len(want)) || obj.Metadata["Last-Modified"] == "" {
			t.Fatalf("body=%q size=%d last-modified=%q", body, obj.Size, obj.Metadata["Last-Modified"])
		}
		if got := counts.get.Load(); got != int64(2*(i+1)) {
			t.Errorf("Get count after read %d = %d, want %d", i+1, got, 2*(i+1))
		}
		if got := counts.link.Load(); got != int64(i+1) {
			t.Errorf("Link count after read %d = %d, want %d", i+1, got, i+1)
		}
	}
	beforeWarmGet, beforeWarmLink := counts.get.Load(), counts.link.Load()
	var firstModified string
	for i := range 2 {
		obj, err := backend.GetObject(ctx, "baseline", "warm.txt", nil)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(obj.Contents)
		if err != nil {
			t.Fatal(err)
		}
		if err := obj.Contents.Close(); err != nil {
			t.Fatal(err)
		}
		if string(body) != "warm contents" {
			t.Fatalf("warm body = %q", body)
		}
		if i == 0 {
			firstModified = obj.Metadata["Last-Modified"]
			changed := time.Unix(1_900_000_000, 0)
			if err := os.Chtimes(filepath.Join(root, "warm.txt"), changed, changed); err != nil {
				t.Fatal(err)
			}
		} else if obj.Metadata["Last-Modified"] == firstModified {
			t.Errorf("Last-Modified stayed at %q after storage mtime changed under cached link", firstModified)
		}
		wantGet, wantLink := beforeWarmGet+int64(i+2), beforeWarmLink+1
		if counts.get.Load() != wantGet || counts.link.Load() != wantLink {
			t.Errorf("cacheable read %d Get/Link = %d/%d, want %d/%d", i+1, counts.get.Load(), counts.link.Load(), wantGet, wantLink)
		}
	}
	t.Logf("cacheable range readers closed after two S3 object closes: %d", counts.readClose.Load())
	partial, err := backend.GetObject(ctx, "baseline", "fixture.txt", &gofakes3.ObjectRangeRequest{Start: 2, End: 5})
	if err != nil {
		t.Fatal(err)
	}
	partialBody, err := io.ReadAll(partial.Contents)
	if err != nil {
		t.Fatal(err)
	}
	if err := partial.Contents.Close(); err != nil {
		t.Fatal(err)
	}
	if string(partialBody) != "seli" || partial.Range == nil || partial.Range.Length != 4 {
		t.Errorf("range body = %q, range = %+v", partialBody, partial.Range)
	}
	childRoot := t.TempDir()
	childAddition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: childRoot})
	if err != nil {
		t.Fatal(err)
	}
	childID, err := op.CreateStorage(ctx, model.Storage{Driver: "Stage0CountedLocal", MountPath: mount + "/virtual/child", Addition: string(childAddition)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(ctx, childID); err != nil {
			t.Errorf("delete virtual fixture storage: %v", err)
		}
	})
	beforeVirtualGet := counts.get.Load()
	beforeVirtualLink := counts.link.Load()
	_, err = backend.GetObject(ctx, "baseline", "virtual", nil)
	if s3ErrorCode(err) != gofakes3.ErrNoSuchKey {
		t.Errorf("virtual directory error = %v, want NoSuchKey", err)
	}
	if counts.get.Load() != beforeVirtualGet || counts.link.Load() != beforeVirtualLink {
		t.Errorf("virtual directory reached driver: Get %d -> %d, Link %d -> %d", beforeVirtualGet, counts.get.Load(), beforeVirtualLink, counts.link.Load())
	}
	for _, name := range []string{"missing.txt", "."} {
		before := counts.link.Load()
		_, err := backend.GetObject(ctx, "baseline", name, nil)
		if s3ErrorCode(err) != gofakes3.ErrNoSuchKey {
			t.Errorf("GetObject(%q) error = %v, want NoSuchKey", name, err)
		}
		if got := counts.link.Load(); got != before {
			t.Errorf("Link count after %q = %d, want %d", name, got, before)
		}
	}
}
