package meilisearch

import (
	"context"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
	"github.com/meilisearch/meilisearch-go"
)

var _ searcher.StatsProvider = (*Meilisearch)(nil)

// GetIndexStats reads committed document statistics, including outstanding local
// updates and remote tasks so an asynchronous update is not reported as complete.
func (m *Meilisearch) GetIndexStats(ctx context.Context) (searcher.IndexStats, error) {
	if m.taskQueue != nil {
		m.taskQueue.mu.RLock()
		pending := len(m.taskQueue.queue) > 0 || m.taskQueue.consuming.Load()
		m.taskQueue.mu.RUnlock()
		if pending {
			return searcher.IndexStats{IsIndexing: true}, nil
		}
	}

	index := m.Client.Index(m.IndexUid)
	tasks, err := index.GetTasksWithContext(ctx, &meilisearch.TasksQuery{
		Limit: 1,
		Statuses: []meilisearch.TaskStatus{
			meilisearch.TaskStatusEnqueued,
			meilisearch.TaskStatusProcessing,
		},
	})
	if err != nil {
		return searcher.IndexStats{}, err
	}
	if len(tasks.Results) > 0 {
		return searcher.IndexStats{IsIndexing: true}, nil
	}

	stats, err := index.GetStatsWithContext(ctx)
	if err != nil {
		return searcher.IndexStats{}, err
	}
	if stats.NumberOfDocuments < 0 {
		return searcher.IndexStats{}, fmt.Errorf("invalid document count: %d", stats.NumberOfDocuments)
	}
	return searcher.IndexStats{
		ObjectCount: uint64(stats.NumberOfDocuments),
		IsIndexing:  stats.IsIndexing,
	}, nil
}
