package tool

import (
	"fmt"
	"sort"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

var (
	Tools = make(ToolsManager)
)

type ToolsManager map[string]Tool

func (t ToolsManager) Get(name string) (Tool, error) {
	if tool, ok := t[name]; ok {
		return tool, nil
	}
	return nil, fmt.Errorf("tool %s not found", name)
}

func (t ToolsManager) Add(tool Tool) {
	t[tool.Name()] = tool
}

func (t ToolsManager) Names() []string {
	names := make([]string, 0, len(t))
	for name := range t {
		if tool, err := t.Get(name); err == nil && tool.IsReady() {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// NamesForPath returns ready tools and the native tool for the destination storage.
// Native tools can write directly to their own storage even without a temporary path setting.
func (t ToolsManager) NamesForPath(path string) []string {
	names := t.Names()
	storage, _, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return names
	}

	name := toolNameForStorage(storage)
	if name == "" {
		return names
	}
	for _, existing := range names {
		if existing == name {
			return names
		}
	}
	if _, ok := t[name]; ok {
		names = append(names, name)
		sort.Strings(names)
	}
	return names
}

func (t ToolsManager) Items() []model.SettingItem {
	var items []model.SettingItem
	for _, tool := range t {
		items = append(items, tool.Items()...)
	}
	return items
}
