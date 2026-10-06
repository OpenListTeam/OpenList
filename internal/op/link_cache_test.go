package op

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

type linkModeDriver struct {
	driver.Driver
	storage model.Storage
	calls   int
}

func (d *linkModeDriver) Config() driver.Config { return driver.Config{} }

func (d *linkModeDriver) GetStorage() *model.Storage { return &d.storage }

func (d *linkModeDriver) Get(context.Context, string) (model.Obj, error) {
	return &model.Object{Name: "file"}, nil
}

func (d *linkModeDriver) Link(_ context.Context, _ model.Obj, args model.LinkArgs) (*model.Link, error) {
	d.calls++
	expiration := time.Minute
	if args.Redirect {
		return &model.Link{URL: "https://example.com/file", Expiration: &expiration}, nil
	}
	return &model.Link{
		RangeReader: stream.RangeReaderFunc(func(context.Context, http_range.Range) (io.ReadCloser, error) {
			return io.NopCloser(strings.NewReader("file")), nil
		}),
		Expiration: &expiration,
	}, nil
}

func TestLinkCacheSeparatesRedirectAndProxy(t *testing.T) {
	for _, tc := range []struct {
		name          string
		firstRedirect bool
	}{
		{name: "redirect then proxy", firstRedirect: true},
		{name: "proxy then redirect", firstRedirect: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &linkModeDriver{storage: model.Storage{MountPath: "/" + t.Name()}}
			for _, redirect := range []bool{tc.firstRedirect, !tc.firstRedirect, tc.firstRedirect, !tc.firstRedirect} {
				link, _, err := Link(context.Background(), d, "/file", model.LinkArgs{Redirect: redirect})
				if err != nil {
					t.Fatal(err)
				}
				if redirect && (link.URL == "" || link.RangeReader != nil) {
					t.Fatalf("redirect link has wrong shape: %+v", link)
				}
				if !redirect && (link.URL != "" || link.RangeReader == nil) {
					t.Fatalf("proxy link has wrong shape: %+v", link)
				}
			}
			if d.calls != 2 {
				t.Fatalf("expected one driver call per mode, got %d", d.calls)
			}
		})
	}
}
