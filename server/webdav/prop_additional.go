package webdav

import (
	"context"
	"encoding/xml"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// additionalLiveProps are live properties that should not be returned for "allprop" PROPFIND requests.
var additionalLiveProps = map[xml.Name]struct {
	// findFn implements the propfind function of this property. If nil,
	// it indicates a hidden property.
	findFn func(context.Context, LockSystem, string, model.Obj) (string, bool, error)
	// showFn indicates whether the prop show be returned for the resource.
	showFn func(model.Obj) bool
}{
	{Space: "DAV:", Local: "quota-available-bytes"}: {
		findFn: findQuotaAvailableBytes,
		showFn: showQuotaAvailableBytes,
	},
	{Space: "DAV:", Local: "quota-used-bytes"}: {
		findFn: findQuotaUsedBytes,
		showFn: showQuotaUsedBytes,
	},
}

func getStorageUsage(ctx context.Context, fi model.Obj) (*model.StorageDetails, error) {
	user := ctx.Value(conf.UserKey).(*model.User)
	reqPath, err := user.JoinPath(fi.GetPath())
	if err != nil {
		return nil, err
	}
	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	if err != nil {
		return nil, err
	}
	details, err := op.GetStorageDetails(ctx, storage)
	if err != nil {
		return nil, err
	}
	return details, nil
}

func showQuotaAvailableBytes(fi model.Obj) bool {
	return fi.IsDir()
}

func findQuotaAvailableBytes(ctx context.Context, ls LockSystem, name string, fi model.Obj) (string, bool, error) {
	if !fi.IsDir() {
		return "", false, nil
	}
	usage, err := getStorageUsage(ctx, fi)
	if err != nil {
		return "", false, err
	}
	if usage == nil {
		return "", false, nil
	}
	available := usage.TotalSpace - usage.UsedSpace
	return strconv.FormatInt(available, 10), true, nil
}

func showQuotaUsedBytes(fi model.Obj) bool {
	return fi.IsDir()
}

func findQuotaUsedBytes(ctx context.Context, ls LockSystem, name string, fi model.Obj) (string, bool, error) {
	if !fi.IsDir() {
		return "", false, nil
	}
	usage, err := getStorageUsage(ctx, fi)
	if err != nil {
		return "", false, err
	}
	if usage == nil {
		return "", false, nil
	}
	return strconv.FormatInt(usage.UsedSpace, 10), true, nil
}
