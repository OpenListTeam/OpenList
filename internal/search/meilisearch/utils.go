package meilisearch

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// hashPath hashes a path with SHA-1.
// Path-relative exact matching should use hash,
// because filtering strings on meilisearch is case-insensitive.
func hashPath(path string) string {
	return utils.HashData(utils.SHA1, []byte(path))
}

func buildSearchDocumentFromResults(results map[string]any) *searchDocument {
	searchNode := model.SearchNode{}
	document := &searchDocument{
		SearchNode: searchNode,
	}

	// use assertion test to avoid panic
	searchNode.Parent, _ = results["parent"].(string)
	searchNode.Name, _ = results["name"].(string)
	searchNode.IsDir, _ = results["is_dir"].(bool)
	searchNode.Size, _ = results["size"].(int64)

	document.ID, _ = results["id"].(string)
	document.ParentHash, _ = results["parent_hash"].(string)
	document.ParentPathHashes, _ = results["parent_path_hashes"].([]string)
	return document
}
