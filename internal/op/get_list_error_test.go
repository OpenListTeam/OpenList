package op

import (
	"context"
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type listErrDriver struct {
	driver.Driver
	storage model.Storage
	listErr error
	files   []model.Obj
}

func (d *listErrDriver) Config() driver.Config { return driver.Config{} }

func (d *listErrDriver) GetStorage() *model.Storage { return &d.storage }

func (d *listErrDriver) GetRoot(context.Context) (model.Obj, error) {
	return &model.Object{Name: "root", IsFolder: true}, nil
}

func (d *listErrDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return d.files, d.listErr
}

func TestGetKeepsListError(t *testing.T) {
	timeout := errors.New("net/http: TLS handshake timeout")
	d := &listErrDriver{storage: model.Storage{MountPath: "/" + t.Name()}, listErr: timeout}
	if _, err := Get(context.Background(), d, "/dir"); !errors.Is(err, timeout) || errs.IsObjectNotFound(err) {
		t.Fatalf("failed listing must not read as a missing object, got %v", err)
	}

	d = &listErrDriver{storage: model.Storage{MountPath: "/" + t.Name() + "-ok"}}
	if _, err := Get(context.Background(), d, "/dir"); !errs.IsObjectNotFound(err) {
		t.Fatalf("missing object: want ObjectNotFound, got %v", err)
	}

	d = &listErrDriver{
		storage: model.Storage{MountPath: "/" + t.Name() + "-file"},
		files:   []model.Obj{&model.Object{Name: "file"}},
	}
	if _, err := Get(context.Background(), d, "/file/child"); !errs.IsObjectNotFound(err) {
		t.Fatalf("path under a file: want ObjectNotFound, got %v", err)
	}
}
