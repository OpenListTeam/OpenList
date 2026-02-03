package search

import (
	"context"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
	log "github.com/sirupsen/logrus"
)

var instance searcher.Searcher = nil

// Init or reset index
func Init(mode string) error {
	if instance != nil {
		// unchanged, do nothing
		if instance.Config().Name == mode {
			return nil
		}
		err := instance.Release(context.Background())
		if err != nil {
			log.Errorf("release instance err: %+v", err)
		}
		instance = nil
	}
	if Running() {
		return fmt.Errorf("index is running")
	}
	if mode == "none" {
		log.Warnf("not enable search")
		return nil
	}
	s, ok := searcher.NewMap[mode]
	if !ok {
		return fmt.Errorf("not support index: %s", mode)
	}
	i, err := s()
	if err != nil {
		log.Errorf("init searcher error: %+v", err)
	} else {
		instance = i
	}
	return err
}

func Search(ctx context.Context, req model.SearchReq) ([]model.SearchNode, int64, error) {
	return instance.Search(ctx, req)
}

func Index(ctx context.Context, parent string, obj model.Obj) error {
	if instance == nil {
		return errs.SearchNotAvailable
	}
	return instance.Index(ctx, model.SearchNode{
		Parent: parent,
		Name:   obj.GetName(),
		IsDir:  obj.IsDir(),
		Size:   obj.GetSize(),
	})
}

type ObjWithParent struct {
	Parent string
	model.Obj
}

// ToSearchNode converts ObjWithParent to model.SearchNode
func (o ObjWithParent) ToSearchNode() model.SearchNode {
	return model.SearchNode{
		Parent: o.Parent,
		Name:   o.GetName(),
		IsDir:  o.IsDir(),
		Size:   o.GetSize(),
	}
}

func BatchIndex(ctx context.Context, objs []ObjWithParent) error {
	if instance == nil {
		return errs.SearchNotAvailable
	}
	if len(objs) == 0 {
		return nil
	}
	searchNodes := make([]model.SearchNode, len(objs))
	for i := range objs {
		searchNodes[i] = objs[i].ToSearchNode()
	}
	return instance.BatchIndex(ctx, searchNodes)
}

func init() {
	op.RegisterSettingItemHook(conf.SearchIndex, func(item *model.SettingItem) error {
		log.Debugf("searcher init, mode: %s", item.Value)
		return Init(item.Value)
	})
}
