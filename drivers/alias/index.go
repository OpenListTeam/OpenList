package alias

import (
	"context"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/search"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type aliasSearchIndexState struct {
	node     model.SearchNode
	exists   bool
	reliable bool
}

func (d *Alias) getResolvedFromSearchIndex(ctx context.Context, roots []string, sub string) (aliasResolved, bool) {
	if sub == "" || !aliasSearchIndexReady() {
		return aliasResolved{}, false
	}
	known := make(map[string]aliasSearchIndexState)
	paths := make([]string, 0, len(roots))
	firstIndex := -1
	for idx, root := range roots {
		rawPath := stdpath.Join(root, sub)
		_, exists, reliable := aliasSearchIndexPath(ctx, rawPath, known)
		if exists {
			if firstIndex < 0 {
				firstIndex = idx
			}
			paths = append(paths, rawPath)
		}
		if !reliable {
			return aliasResolved{}, false
		}
	}
	if len(paths) == 0 {
		return aliasResolved{}, false
	}
	return aliasResolved{
		paths:   paths,
		skipped: firstIndex > 0,
	}, true
}

func aliasSearchIndexReady() bool {
	if setting.GetStr(conf.SearchIndex) == "none" {
		return false
	}
	if search.Running() {
		return false
	}
	progress, err := search.Progress()
	return err == nil && progress.IsDone && progress.Error == ""
}

func aliasSearchIndexPath(ctx context.Context, rawPath string, known map[string]aliasSearchIndexState) (node model.SearchNode, exists, reliable bool) {
	rawPath = utils.FixAndCleanPath(rawPath)
	if rawPath == "/" {
		return model.SearchNode{
			Parent: "/",
			Name:   "",
			IsDir:  true,
		}, true, true
	}
	if state, ok := known[rawPath]; ok {
		return state.node, state.exists, state.reliable
	}
	parent := stdpath.Dir(rawPath)
	name := stdpath.Base(rawPath)
	nodes, err := search.Get(ctx, parent)
	if err != nil {
		known[rawPath] = aliasSearchIndexState{}
		return model.SearchNode{}, false, false
	}
	for _, node := range nodes {
		if node.Name == name {
			known[rawPath] = aliasSearchIndexState{
				node:     node,
				exists:   true,
				reliable: true,
			}
			return node, true, true
		}
	}
	if len(nodes) > 0 {
		known[rawPath] = aliasSearchIndexState{
			exists:   false,
			reliable: true,
		}
		return model.SearchNode{}, false, true
	}
	_, _, parentReliable := aliasSearchIndexPath(ctx, parent, known)
	if parentReliable {
		known[rawPath] = aliasSearchIndexState{
			exists:   false,
			reliable: true,
		}
		return model.SearchNode{}, false, true
	}
	known[rawPath] = aliasSearchIndexState{}
	return model.SearchNode{}, false, false
}
