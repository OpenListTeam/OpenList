package meilisearch

import (
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// hashPath hashes a path with SHA-1.
// Path-relative exact matching should use hash,
// because filtering strings on meilisearch is case-insensitive.
func hashPath(path string) string {
	return utils.HashData(utils.SHA1, []byte(path))
}

func buildSearchDocumentFromResults(results map[string]any) *searchDocument {
	document := &searchDocument{}

	// use assertion test to avoid panic
	document.SearchNode.Parent, _ = results["parent"].(string)
	document.SearchNode.Name, _ = results["name"].(string)
	document.SearchNode.IsDir, _ = results["is_dir"].(bool)
	// JSON numbers are typically float64, not int64
	if size, ok := results["size"].(float64); ok {
		document.SearchNode.Size = int64(size)
	}

	document.ID, _ = results["id"].(string)
	document.ParentHash, _ = results["parent_hash"].(string)
	document.ParentPathHashes, _ = results["parent_path_hashes"].([]string)
	return document
}
