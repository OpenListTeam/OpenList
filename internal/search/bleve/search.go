package bleve

import (
	"context"
	"os"

	query2 "github.com/blevesearch/bleve/v2/search/query"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/blevesearch/bleve/v2"
	search2 "github.com/blevesearch/bleve/v2/search"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

type Bleve struct {
	BIndex bleve.Index
}

func (b *Bleve) Config() searcher.Config {
	return config
}

func (b *Bleve) Search(ctx context.Context, req model.SearchReq) ([]model.SearchNode, int64, error) {
	reqQuery := buildQuery(req)
	search := bleve.NewSearchRequest(reqQuery)
	search.SortBy([]string{"name", "_id"})
	search.From = (req.Page - 1) * req.PerPage
	search.Size = req.PerPage
	search.Fields = []string{"*"}
	searchResults, err := b.BIndex.Search(search)
	if err != nil {
		log.Errorf("search error: %+v", err)
		return nil, 0, err
	}
	res, err := utils.SliceConvert(searchResults.Hits, func(src *search2.DocumentMatch) (model.SearchNode, error) {
		return searchNodeFromHit(src), nil
	})
	return res, int64(searchResults.Total), err
}

const searchBatchSize = 1000

func (b *Bleve) SearchFiltered(ctx context.Context, req model.SearchReq, filter searcher.Filter) ([]model.SearchNode, int64, error) {
	reqQuery := buildQuery(req)
	from := int64(req.Page-1) * int64(req.PerPage)
	to := from + int64(req.PerPage)
	var (
		result      []model.SearchNode
		total       int64
		searchAfter []string
	)
	for {
		search := bleve.NewSearchRequest(reqQuery)
		search.SortBy([]string{"name", "_id"})
		search.Size = searchBatchSize
		search.Fields = []string{"*"}
		if searchAfter != nil {
			search.SetSearchAfter(searchAfter)
		}
		searchResults, err := b.BIndex.Search(search)
		if err != nil {
			log.Errorf("search error: %+v", err)
			return nil, 0, err
		}
		for _, hit := range searchResults.Hits {
			node := searchNodeFromHit(hit)
			if !utils.IsSubPath(req.Parent, node.Parent) || filter != nil && !filter(node) {
				continue
			}
			if total >= from && total < to {
				result = append(result, node)
			}
			total++
		}
		if len(searchResults.Hits) < searchBatchSize {
			break
		}
		last := searchResults.Hits[len(searchResults.Hits)-1]
		searchAfter = append(searchAfter[:0], last.Sort...)
	}
	return result, total, nil
}

func buildQuery(req model.SearchReq) query2.Query {
	var queries []query2.Query
	query := bleve.NewMatchQuery(req.Keywords)
	query.SetField("name")
	queries = append(queries, query)
	if req.Scope != 0 {
		isDir := req.Scope == 1
		isDirQuery := bleve.NewBoolFieldQuery(isDir)
		queries = append(queries, isDirQuery)
	}
	return bleve.NewConjunctionQuery(queries...)
}

func searchNodeFromHit(src *search2.DocumentMatch) model.SearchNode {
	return model.SearchNode{
		Parent: src.Fields["parent"].(string),
		Name:   src.Fields["name"].(string),
		IsDir:  src.Fields["is_dir"].(bool),
		Size:   int64(src.Fields["size"].(float64)),
	}
}

func (b *Bleve) Index(ctx context.Context, node model.SearchNode) error {
	return b.BIndex.Index(uuid.NewString(), node)
}

func (b *Bleve) BatchIndex(ctx context.Context, nodes []model.SearchNode) error {
	batch := b.BIndex.NewBatch()
	for _, node := range nodes {
		batch.Index(uuid.NewString(), node)
	}
	return b.BIndex.Batch(batch)
}

func (b *Bleve) Get(ctx context.Context, parent string) ([]model.SearchNode, error) {
	return nil, errs.NotSupport
}

func (b *Bleve) Del(ctx context.Context, prefix string) error {
	return errs.NotSupport
}

func (b *Bleve) Release(ctx context.Context) error {
	if b.BIndex != nil {
		return b.BIndex.Close()
	}
	return nil
}

func (b *Bleve) Clear(ctx context.Context) error {
	err := b.Release(ctx)
	if err != nil {
		return err
	}
	log.Infof("Removing old index...")
	err = os.RemoveAll(conf.Conf.BleveDir)
	if err != nil {
		log.Errorf("clear bleve error: %+v", err)
	}
	bIndex, err := Init(&conf.Conf.BleveDir)
	if err != nil {
		return err
	}
	b.BIndex = bIndex
	return nil
}

var _ searcher.Searcher = (*Bleve)(nil)
var _ searcher.FilteredSearcher = (*Bleve)(nil)
