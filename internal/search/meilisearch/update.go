package meilisearch

import (
	"context"
	"errors"
	"fmt"
	"path"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
	"github.com/meilisearch/meilisearch-go"
)

func (m *Meilisearch) UpdateSnapshot(ctx context.Context, parent string, objs []model.Obj) error {
	nodes, err := m.Get(ctx, parent)
	if err != nil {
		return fmt.Errorf("get indexed nodes: %w", err)
	}

	removed, added := searcher.SnapshotDiff(objs, nodes)
	pathsToDelete := make([]string, 0, len(removed))
	for _, node := range removed {
		objPath := path.Join(parent, node.Name)
		if !op.HasStorage(objPath) {
			pathsToDelete = append(pathsToDelete, objPath)
		}
	}

	var (
		taskUIDs   []int64
		updateErrs []error
	)
	deleteUIDs, err := m.batchDeleteWithTaskUID(ctx, pathsToDelete)
	if err != nil {
		updateErrs = append(updateErrs, fmt.Errorf("delete stale nodes: %w", err))
	} else {
		taskUIDs = append(taskUIDs, deleteUIDs...)
	}

	nodesToAdd := make([]model.SearchNode, 0, len(added))
	for _, obj := range added {
		nodesToAdd = append(nodesToAdd, model.SearchNode{Parent: parent, Name: obj.GetName(), IsDir: obj.IsDir(), Size: obj.GetSize()})
	}
	addUIDs, err := m.batchIndexWithTaskUID(ctx, nodesToAdd)
	if err != nil {
		updateErrs = append(updateErrs, fmt.Errorf("index new nodes: %w", err))
	} else {
		taskUIDs = append(taskUIDs, addUIDs...)
	}

	for _, taskUID := range taskUIDs {
		status, err := m.getTaskStatus(ctx, taskUID)
		if err != nil {
			updateErrs = append(updateErrs, fmt.Errorf("wait for task %d: %w", taskUID, err))
			continue
		}
		if status != meilisearch.TaskStatusSucceeded {
			updateErrs = append(updateErrs, fmt.Errorf("task %d completed with status %s", taskUID, status))
		}
	}
	return errors.Join(updateErrs...)
}
