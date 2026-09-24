package searcher

import "github.com/OpenListTeam/OpenList/v4/internal/model"

func SnapshotDiff(objs []model.Obj, nodes []model.SearchNode) (removed []model.SearchNode, added []model.Obj) {
	current := make(map[string]struct{}, len(objs))
	for _, obj := range objs {
		current[obj.GetName()] = struct{}{}
	}
	indexed := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		indexed[node.Name] = struct{}{}
		if _, ok := current[node.Name]; !ok {
			removed = append(removed, node)
		}
	}
	for _, obj := range objs {
		if _, ok := indexed[obj.GetName()]; !ok {
			added = append(added, obj)
		}
	}
	return
}
