package search

import (
	"context"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
)

type filteredSearchStub struct {
	nodes []model.SearchNode
}

func (s *filteredSearchStub) Config() searcher.Config {
	return searcher.Config{Name: "stub"}
}

func (s *filteredSearchStub) Search(_ context.Context, req model.SearchReq) ([]model.SearchNode, int64, error) {
	from := (req.Page - 1) * req.PerPage
	if from >= len(s.nodes) {
		return nil, int64(len(s.nodes)), nil
	}
	to := min(from+req.PerPage, len(s.nodes))
	return s.nodes[from:to], int64(len(s.nodes)), nil
}

func (s *filteredSearchStub) Index(context.Context, model.SearchNode) error        { return nil }
func (s *filteredSearchStub) BatchIndex(context.Context, []model.SearchNode) error { return nil }
func (s *filteredSearchStub) Get(context.Context, string) ([]model.SearchNode, error) {
	return nil, nil
}
func (s *filteredSearchStub) Del(context.Context, string) error { return nil }
func (s *filteredSearchStub) Release(context.Context) error     { return nil }
func (s *filteredSearchStub) Clear(context.Context) error       { return nil }

func TestSearchFilteredFiltersBeforePagination(t *testing.T) {
	previous := instance
	instance = &filteredSearchStub{nodes: []model.SearchNode{
		{Name: "denied-1"},
		{Name: "allowed-1"},
		{Name: "denied-2"},
		{Name: "allowed-2"},
	}}
	t.Cleanup(func() { instance = previous })

	nodes, total, err := SearchFiltered(context.Background(), model.SearchReq{
		PageReq: model.PageReq{Page: 2, PerPage: 1},
	}, func(node model.SearchNode) bool {
		return node.Name == "allowed-1" || node.Name == "allowed-2"
	})
	if err != nil {
		t.Fatalf("SearchFiltered() error = %v", err)
	}
	if total != 2 {
		t.Fatalf("SearchFiltered() total = %d, want 2", total)
	}
	if len(nodes) != 1 || nodes[0].Name != "allowed-2" {
		t.Fatalf("SearchFiltered() nodes = %#v, want allowed-2", nodes)
	}
}
