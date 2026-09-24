package task_group

import (
	"context"
	"sync"
)

type transferGroup struct {
	pending      int
	hasSuccess   bool
	removeSource map[string]struct{}
}

type TransferGroupCoordinator struct {
	mu     sync.Mutex
	groups map[string]*transferGroup
}

func (coordinator *TransferGroupCoordinator) AddTask(groupID string) {
	coordinator.mu.Lock()
	group := coordinator.groups[groupID]
	if group == nil {
		group = &transferGroup{removeSource: make(map[string]struct{})}
		coordinator.groups[groupID] = group
	}
	group.pending++
	coordinator.mu.Unlock()
}

func (coordinator *TransferGroupCoordinator) RemoveSource(groupID, path string) {
	coordinator.mu.Lock()
	if group := coordinator.groups[groupID]; group != nil {
		group.removeSource[path] = struct{}{}
	}
	coordinator.mu.Unlock()
}

func (coordinator *TransferGroupCoordinator) Done(ctx context.Context, groupID string, success bool) {
	coordinator.mu.Lock()
	group := coordinator.groups[groupID]
	if group == nil || group.pending == 0 {
		coordinator.mu.Unlock()
		return
	}
	group.hasSuccess = group.hasSuccess || success
	group.pending--
	if group.pending != 0 {
		coordinator.mu.Unlock()
		return
	}
	delete(coordinator.groups, groupID)
	coordinator.mu.Unlock()
	if group.hasSuccess {
		finalizeTransferGroup(ctx, groupID, group)
	}
}
