package db

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
)

var config = searcher.Config{
	Name:       "database",
	AutoUpdate: true,
}

func init() {
	searcher.RegisterSearcher(config, func() (searcher.Searcher, error) {
		rwDb := db.GetDb()
		switch conf.Conf.Database.Type {
		case "mysql":
			tableName := fmt.Sprintf("%ssearch_nodes", conf.Conf.Database.TablePrefix)
			tx := rwDb.W().Exec(fmt.Sprintf("CREATE FULLTEXT INDEX idx_%s_name_fulltext ON %s(name);", tableName, tableName))
			if err := tx.Error; err != nil && !strings.Contains(err.Error(), "Error 1061 (42000)") { // duplicate error
				log.Errorf("failed to create full text index: %v", err)
				return nil, err
			}
		case "postgres":
			rwDb.W().Exec("CREATE EXTENSION pg_trgm;")
			rwDb.W().Exec("CREATE EXTENSION btree_gin;")
			tableName := fmt.Sprintf("%ssearch_nodes", conf.Conf.Database.TablePrefix)
			tx := rwDb.W().Exec(fmt.Sprintf("CREATE INDEX idx_%s_name ON %s USING GIN (name);", tableName, tableName))
			if err := tx.Error; err != nil && !strings.Contains(err.Error(), "SQLSTATE 42P07") {
				log.Errorf("failed to create index using GIN: %v", err)
				return nil, err
			}
		}
		return &DB{}, nil
	})
}
