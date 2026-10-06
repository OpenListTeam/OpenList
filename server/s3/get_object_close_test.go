package s3

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"gorm.io/gorm"
)

const closeTrackingDriverName = "S3CloseTrackingLocal"

type closeTrackingDriver struct {
	local.Local
	closed *[]string
}

func (d *closeTrackingDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = closeTrackingDriverName
	return c
}

func (d *closeTrackingDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	link := &model.Link{
		ContentLength: 4,
		RangeReader: stream.RangeReaderFunc(func(context.Context, http_range.Range) (io.ReadCloser, error) {
			return utils.NewReadCloser(strings.NewReader("body"), func() error {
				*d.closed = append(*d.closed, "body")
				return nil
			}), nil
		}),
		RequireReference: true,
	}
	link.SyncClosers.Add(utils.CloseFunc(func() error {
		*d.closed = append(*d.closed, "link")
		return nil
	}))
	return link, nil
}

func TestGetObjectClosesRangeBodyBeforeLink(t *testing.T) {
	ctx := context.Background()
	var closed []string
	op.RegisterDriver(func() driver.Driver {
		return &closeTrackingDriver{closed: &closed}
	})

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.txt"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	addition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	mount := "/" + sanitizeTestName(t.Name())
	storageID, err := op.CreateStorage(ctx, model.Storage{
		Driver:    closeTrackingDriverName,
		MountPath: mount,
		Addition:  string(addition),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(ctx, storageID); err != nil {
			t.Errorf("delete fixture storage: %v", err)
		}
	})

	previousBuckets, previousBucketsErr := op.GetSettingItemByKey(conf.S3Buckets)
	if previousBucketsErr != nil && !errors.Is(previousBucketsErr, gorm.ErrRecordNotFound) {
		t.Fatal(previousBucketsErr)
	}
	if err := op.SaveSettingItem(&model.SettingItem{
		Key:   conf.S3Buckets,
		Value: `[{"name":"close","path":"` + mount + `"}]`,
	}); err != nil {
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

	object, err := newBackend().(*s3Backend).GetObject(ctx, "close", "fixture.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := io.ReadAll(object.Contents)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "body" {
		t.Fatalf("contents = %q, want body", contents)
	}
	if err := object.Contents.Close(); err != nil {
		t.Fatal(err)
	}
	if err := object.Contents.Close(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closed, []string{"body", "link"}) {
		t.Fatalf("close order = %v, want [body link] exactly once", closed)
	}
}
