package task_group

import (
	"sync"

	"github.com/sirupsen/logrus"
)

type OnCompletionFunc func(groupID string, payloads ...any)
type TaskGroupCoordinator struct {
	name string
	mu   sync.Mutex

	groupPayloads map[string][]any
	groupStates   map[string]groupState
	onCompletion  OnCompletionFunc
}

type groupState struct {
	pending    int
	hasSuccess bool
}

func NewTaskGroupCoordinator(name string, f OnCompletionFunc) *TaskGroupCoordinator {
	return &TaskGroupCoordinator{
		name:          name,
		groupPayloads: map[string][]any{},
		groupStates:   map[string]groupState{},
		onCompletion:  f,
	}
}

// payload可为nil
func (tgc *TaskGroupCoordinator) AddTask(groupID string, payload any) {
	tgc.mu.Lock()
	defer tgc.mu.Unlock()
	state := tgc.groupStates[groupID]
	state.pending++
	tgc.groupStates[groupID] = state
	logrus.Debugf("AddTask:%s ,count=%+v", groupID, state)
	if payload == nil {
		return
	}
	tgc.groupPayloads[groupID] = append(tgc.groupPayloads[groupID], payload)
}

func (tgc *TaskGroupCoordinator) AppendPayload(groupID string, payload any) {
	if payload == nil {
		return
	}
	tgc.mu.Lock()
	defer tgc.mu.Unlock()
	tgc.groupPayloads[groupID] = append(tgc.groupPayloads[groupID], payload)
}

func (tgc *TaskGroupCoordinator) Done(groupID string, success bool) {
	tgc.mu.Lock()
	defer tgc.mu.Unlock()
	state, ok := tgc.groupStates[groupID]
	if !ok || state.pending == 0 {
		return
	}
	if success {
		state.hasSuccess = true
	}
	logrus.Debugf("Done:%s ,state=%+v", groupID, state)
	if state.pending == 1 {
		payloads := tgc.groupPayloads[groupID]
		delete(tgc.groupStates, groupID)
		delete(tgc.groupPayloads, groupID)
		if tgc.onCompletion != nil && state.hasSuccess {
			logrus.Debugf("OnCompletion:%s", groupID)
			tgc.mu.Unlock()
			tgc.onCompletion(groupID, payloads...)
			tgc.mu.Lock()
		}
		return
	}
	state.pending--
	tgc.groupStates[groupID] = state
}
