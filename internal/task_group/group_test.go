package task_group

import (
	"context"
	"testing"
)

func TestTransferGroupKeepsAnySuccessUntilTerminalCompletion(t *testing.T) {
	coordinator := &TransferGroupCoordinator{groups: map[string]*transferGroup{}}
	const groupID = "/missing-destination"
	coordinator.AddTask(groupID)
	coordinator.AddTask(groupID)
	coordinator.RemoveSource(groupID, "/source")
	coordinator.RemoveSource(groupID, "/source")

	coordinator.Done(context.Background(), groupID, true)
	group := coordinator.groups[groupID]
	if group == nil || group.pending != 1 || !group.hasSuccess || len(group.removeSource) != 1 {
		t.Fatalf("intermediate group state = %+v", group)
	}

	coordinator.Done(context.Background(), groupID, false)
	if coordinator.groups[groupID] != nil {
		t.Fatal("terminal group state was not released")
	}
}
