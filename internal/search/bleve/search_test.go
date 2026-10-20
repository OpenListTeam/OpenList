package bleve

import (
	"context"
	"fmt"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	blevelib "github.com/blevesearch/bleve/v2"
)

func TestSearchFilteredKeepsDuplicateSortValuesAcrossBatches(t *testing.T) {
	indexMapping := blevelib.NewIndexMapping()
	searchNodeMapping := blevelib.NewDocumentMapping()
	searchNodeMapping.AddFieldMappingsAt("is_dir", blevelib.NewBooleanFieldMapping())
	searchNodeMapping.AddFieldMappingsAt("parent", blevelib.NewTextFieldMapping())
	searchNodeMapping.AddFieldMappingsAt("name", blevelib.NewKeywordFieldMapping())
	indexMapping.AddDocumentMapping("SearchNode", searchNodeMapping)
	index, err := blevelib.NewMemOnly(indexMapping)
	if err != nil {
		t.Fatalf("NewMemOnly() error = %v", err)
	}
	t.Cleanup(func() { _ = index.Close() })

	batch := index.NewBatch()
	for i := 0; i < searchBatchSize+1; i++ {
		batch.Index(fmt.Sprintf("allowed-%04d", i), model.SearchNode{
			Parent: "/base",
			Name:   "duplicate",
		})
	}
	batch.Index("denied", model.SearchNode{Parent: "/base2", Name: "duplicate"})
	if err := index.Batch(batch); err != nil {
		t.Fatalf("Batch() error = %v", err)
	}

	b := &Bleve{BIndex: index}
	nodes, total, err := b.SearchFiltered(context.Background(), model.SearchReq{
		Parent:   "/base",
		Keywords: "duplicate",
		PageReq:  model.PageReq{Page: 1, PerPage: searchBatchSize + 1},
	}, nil)
	if err != nil {
		t.Fatalf("SearchFiltered() error = %v", err)
	}
	if total != searchBatchSize+1 {
		t.Fatalf("SearchFiltered() total = %d, want %d", total, searchBatchSize+1)
	}
	if len(nodes) != searchBatchSize+1 {
		t.Fatalf("SearchFiltered() returned %d nodes, want %d", len(nodes), searchBatchSize+1)
	}
}
