package op_test

import (
	"context"
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// A driver with only the required read behavior has no mutation or archive
// capability. The operation boundary must retain the old NotImplement result.
type readOnlyCapabilityDriver struct{ model.Storage }

func (*readOnlyCapabilityDriver) Config() driver.Config          { return driver.Config{NoCache: true} }
func (*readOnlyCapabilityDriver) GetAddition() driver.Additional { return nil }
func (*readOnlyCapabilityDriver) Init(context.Context) error     { return nil }
func (*readOnlyCapabilityDriver) Drop(context.Context) error     { return nil }
func (*readOnlyCapabilityDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}
func (*readOnlyCapabilityDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, nil
}
func (*readOnlyCapabilityDriver) Get(_ context.Context, path string) (model.Obj, error) {
	switch path {
	case "/src":
		return &model.Object{Name: "src", Path: path}, nil
	case "/dst":
		return &model.Object{Name: "dst", Path: path, IsFolder: true}, nil
	default:
		return nil, errs.ObjectNotFound
	}
}

func TestAbsentDriverCapabilitiesReturnNotImplement(t *testing.T) {
	storage := &readOnlyCapabilityDriver{Storage: model.Storage{MountPath: "/capability-test"}}
	ctx := context.Background()
	tests := []struct {
		name string
		run  func() error
	}{
		{"copy", func() error { return op.Copy(ctx, storage, "/src", "/dst") }},
		{"archive decompress", func() error {
			return op.ArchiveDecompress(ctx, storage, "/src", "/dst", model.ArchiveDecompressArgs{})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, errs.NotImplement) {
				t.Fatalf("got %v, want NotImplement", err)
			}
		})
	}
}
