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

const searchBatchSize = 1000

func SearchFiltered(ctx context.Context, req model.SearchReq, filter searcher.Filter) ([]model.SearchNode, int64, error) {
	if filteredSearcher, ok := instance.(searcher.FilteredSearcher); ok {
		return filteredSearcher.SearchFiltered(ctx, req, filter)
	}

	batchReq := req
	batchReq.Page = 1
	batchReq.PerPage = searchBatchSize
	from := int64(req.Page-1) * int64(req.PerPage)
	to := from + int64(req.PerPage)
	var (
		result         []model.SearchNode
		filteredTotal  int64
		processedTotal int64
	)
	for {
		nodes, total, err := instance.Search(ctx, batchReq)
		if err != nil {
			return nil, 0, err
		}
		for _, node := range nodes {
			if filter != nil && !filter(node) {
				continue
			}
			if filteredTotal >= from && filteredTotal < to {
				result = append(result, node)
			}
			filteredTotal++
		}
		processedTotal += int64(len(nodes))
		if len(nodes) == 0 || processedTotal >= total {
			break
		}
		batchReq.Page++
	}
	return result, filteredTotal, nil
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

func BatchIndex(ctx context.Context, objs []ObjWithParent) error {
	if instance == nil {
		return errs.SearchNotAvailable
	}
	if len(objs) == 0 {
		return nil
	}
	var searchNodes []model.SearchNode
	for i := range objs {
		searchNodes = append(searchNodes, model.SearchNode{
			Parent: objs[i].Parent,
			Name:   objs[i].GetName(),
			IsDir:  objs[i].IsDir(),
			Size:   objs[i].GetSize(),
		})
	}
	return instance.BatchIndex(ctx, searchNodes)
}

func init() {
	op.RegisterSettingItemHook(conf.SearchIndex, func(item *model.SettingItem) error {
		log.Debugf("searcher init, mode: %s", item.Value)
		return Init(item.Value)
	})
}
