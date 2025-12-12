package plugin_warp

import (
	"context"

	witgo "github.com/OpenListTeam/wazero-wasip2/wit-go"
)

type ContextManaget = witgo.ResourceManager[context.Context]
type Context = uint32

func NewContextManager() *ContextManaget {
	return witgo.NewResourceManager[context.Context](nil)
}

type ContextPollable struct {
	context.Context
}

func (c *ContextPollable) IsReady() bool {
	select {
	case <-c.Done():
		return true
	default:
		return false
	}
}

// Block 阻塞直到 Pollable 就绪。
func (c *ContextPollable) Block() {
	<-c.Done()
}

func (*ContextPollable) SetReady() {

}

func (ContextPollable) Close() {

}

func (c *ContextPollable) Channel() <-chan struct{} {
	return c.Done()
}
