package meilisearch

import (
	"errors"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/meilisearch/meilisearch-go"
)

var config = searcher.Config{
	Name:       "meilisearch",
	AutoUpdate: true,
}

func init() {
	searcher.RegisterSearcher(config, func() (searcher.Searcher, error) {
		indexUid := conf.Conf.Meilisearch.Index
		if len(indexUid) == 0 {
			return nil, errors.New("index is blank")
		}
		m := Meilisearch{
			Client: meilisearch.New(
				conf.Conf.Meilisearch.Host,
				meilisearch.WithAPIKey(conf.Conf.Meilisearch.APIKey),
			),
			IndexUid: indexUid,
			FilterableAttributes: []string{"parent", "is_dir", "name",
				"parent_hash", "parent_path_hashes"},
			SearchableAttributes: []string{"name"},
		}

		_, err := m.Client.GetIndex(m.IndexUid)
		if err != nil {
			var mErr *meilisearch.Error
			ok := errors.As(err, &mErr)
			if ok && mErr.MeilisearchApiError.Code == "index_not_found" {
				task, err := m.Client.CreateIndex(&meilisearch.IndexConfig{
					Uid:        m.IndexUid,
					PrimaryKey: "id",
				})
				if err != nil {
					return nil, err
				}
				forTask, err := m.Client.WaitForTask(task.TaskUID, time.Second)
				if err != nil {
					return nil, err
				}
				if forTask.Status != meilisearch.TaskStatusSucceeded {
					return nil, fmt.Errorf("index creation failed, task status is %s", forTask.Status)
				}
			} else {
				return nil, err
			}
		}
		attributes, err := m.Client.Index(m.IndexUid).GetFilterableAttributes()
		if err != nil {
			return nil, err
		}
		if attributes == nil || !utils.SliceAllContains(*attributes, m.FilterableAttributes...) {
			_, err = m.Client.Index(m.IndexUid).UpdateFilterableAttributes(&m.FilterableAttributes)
			if err != nil {
				return nil, err
			}
		}

		attributes, err = m.Client.Index(m.IndexUid).GetSearchableAttributes()
		if err != nil {
			return nil, err
		}
		if attributes == nil || !utils.SliceAllContains(*attributes, m.SearchableAttributes...) {
			_, err = m.Client.Index(m.IndexUid).UpdateSearchableAttributes(&m.SearchableAttributes)
			if err != nil {
				return nil, err
			}
		}

		pagination, err := m.Client.Index(m.IndexUid).GetPagination()
		if err != nil {
			return nil, err
		}
		if pagination.MaxTotalHits != int64(model.MaxInt) {
			_, err := m.Client.Index(m.IndexUid).UpdatePagination(&meilisearch.Pagination{
				MaxTotalHits: int64(model.MaxInt),
			})
			if err != nil {
				return nil, err
			}
		}

		// Initialize and start task queue manager
		m.taskQueue = NewTaskQueueManager(&m)
		m.taskQueue.Start()

		return &m, nil
	})
}
