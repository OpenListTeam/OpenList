package aria2

import (
	"github.com/OpenListTeam/OpenList/v4/pkg/aria2/rpc"
	"github.com/OpenListTeam/OpenList/v4/pkg/generic_sync"
)

const (
	Downloading = iota
	Paused
	Stopped
	Completed
	Errored
)

type Notify struct {
	Signals generic_sync.MapOf[string, chan int]
}

func NewNotify() *Notify {
	return &Notify{Signals: generic_sync.MapOf[string, chan int]{}}
}

func (n *Notify) OnDownloadStart(events []rpc.Event) {
	n.send(events, Downloading)
}

func (n *Notify) OnDownloadPause(events []rpc.Event) {
	n.send(events, Paused)
}

func (n *Notify) OnDownloadStop(events []rpc.Event) {
	n.send(events, Stopped)
}

func (n *Notify) OnDownloadComplete(events []rpc.Event) {
	n.send(events, Completed)
}

func (n *Notify) OnDownloadError(events []rpc.Event) {
	n.send(events, Errored)
}

func (n *Notify) OnBtDownloadComplete(events []rpc.Event) {
	n.send(events, Completed)
}

func (n *Notify) send(events []rpc.Event, status int) {
	for _, e := range events {
		if signal, ok := n.Signals.Load(e.Gid); ok {
			// Notifications and RPC replies share the WebSocket reader. A task
			// may already have exited or be waiting for its cleanup reply. Status
			// polling supplies the fallback when this wake-up is dropped.
			select {
			case signal <- status:
			default:
			}
		}
	}
}
