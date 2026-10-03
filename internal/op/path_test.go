package op

import (
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type pathResolutionDriver struct {
	model.Storage
}

func (d *pathResolutionDriver) Config() driver.Config {
	return driver.Config{}
}

func (d *pathResolutionDriver) GetAddition() driver.Additional {
	return nil
}

func (*pathResolutionDriver) Init(context.Context) error {
	return nil
}

func (*pathResolutionDriver) Drop(context.Context) error {
	return nil
}

func (*pathResolutionDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}

func (*pathResolutionDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, nil
}

func TestGetStorageAndActualPathByMountPathPinsBalanceMount(t *testing.T) {
	mountPath := "/path-resolution-test"
	balancedMountPath := mountPath + ".balance"
	storage := &pathResolutionDriver{Storage: model.Storage{MountPath: balancedMountPath}}
	storagesMap.Store(balancedMountPath, storage)
	defer storagesMap.Delete(balancedMountPath)

	gotStorage, gotPath, err := GetStorageAndActualPathByMountPath(mountPath+"/downloads", balancedMountPath)
	if err != nil {
		t.Fatalf("GetStorageAndActualPathByMountPath() error = %v", err)
	}
	if gotStorage != storage {
		t.Fatalf("resolved storage = %p, want pinned storage %p", gotStorage, storage)
	}
	if gotPath != "/downloads" {
		t.Fatalf("resolved actual path = %q, want %q", gotPath, "/downloads")
	}
}

func TestGetStorageAndActualPathByMountPathRejectsOutsidePath(t *testing.T) {
	mountPath := "/path-resolution-outside-test"
	storage := &pathResolutionDriver{Storage: model.Storage{MountPath: mountPath}}
	storagesMap.Store(mountPath, storage)
	defer storagesMap.Delete(mountPath)

	if _, _, err := GetStorageAndActualPathByMountPath(mountPath+"-other/file", mountPath); err == nil {
		t.Fatal("GetStorageAndActualPathByMountPath() accepted a path outside the pinned mount")
	}
}
