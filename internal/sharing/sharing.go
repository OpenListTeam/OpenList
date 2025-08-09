package sharing

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/go-cache"
	log "github.com/sirupsen/logrus"
)

var (
	AccessCache      = cache.NewMemCache[interface{}]()
	AccessCountDelay = 30 * time.Minute
)

func countAccess(ip string, s *model.Sharing) error {
	key := fmt.Sprintf("%s:%s", s.ID, ip)
	_, ok := AccessCache.Get(key)
	if !ok {
		AccessCache.Set(key, struct{}{}, cache.WithEx[interface{}](AccessCountDelay))
		s.Accessed += 1
		return op.UpdateSharing(s, true)
	}
	return nil
}

func List(ctx context.Context, sid, path string, args model.SharingListArgs) (*model.Sharing, []model.Obj, error) {
	sharing, res, err := list(ctx, sid, path, args)
	if err != nil {
		log.Errorf("failed list sharing %s/%s: %+v", sid, path, err)
		return nil, nil, err
	}
	_ = countAccess(ctx.Value(conf.ClientIPKey).(string), sharing)
	return sharing, res, nil
}

func Get(ctx context.Context, sid, path string, args model.SharingListArgs) (*model.Sharing, model.Obj, error) {
	sharing, res, err := get(ctx, sid, path, args)
	if err != nil {
		log.Warnf("failed get sharing %s/%s: %s", sid, path, err)
		return nil, nil, err
	}
	_ = countAccess(ctx.Value(conf.ClientIPKey).(string), sharing)
	return sharing, res, nil
}

func ArchiveMeta(ctx context.Context, sid, path string, args model.SharingArchiveMetaArgs) (*model.Sharing, *model.ArchiveMetaProvider, error) {
	sharing, res, err := archiveMeta(ctx, sid, path, args)
	if err != nil {
		log.Warnf("failed get sharing archive meta %s/%s: %s", sid, path, err)
		return nil, nil, err
	}
	_ = countAccess(ctx.Value(conf.ClientIPKey).(string), sharing)
	return sharing, res, nil
}

func ArchiveList(ctx context.Context, sid, path string, args model.SharingArchiveListArgs) (*model.Sharing, []model.Obj, error) {
	sharing, res, err := archiveList(ctx, sid, path, args)
	if err != nil {
		log.Warnf("failed list sharing archive %s/%s: %s", sid, path, err)
		return nil, nil, err
	}
	_ = countAccess(ctx.Value(conf.ClientIPKey).(string), sharing)
	return sharing, res, nil
}

type LinkArgs struct {
	model.SharingListArgs
	model.LinkArgs
}

func Link(ctx context.Context, sid, path string, args *LinkArgs) (driver.Driver, *model.Link, model.Obj, error) {
	sharing, storage, res, file, err := link(ctx, sid, path, args)
	if err != nil {
		log.Errorf("failed get sharing link %s/%s: %+v", sid, path, err)
		return nil, nil, nil, err
	}
	_ = countAccess(ctx.Value(conf.ClientIPKey).(string), sharing)
	return storage, res, file, nil
}
