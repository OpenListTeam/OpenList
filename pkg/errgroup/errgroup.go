package errgroup

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/avast/retry-go"
)

type token struct{}
type Group struct {
	cancel func(error)
	ctx    context.Context
	opts   []retry.Option

	success uint64

	wg  sync.WaitGroup
	sem chan token

	startChan chan token
}

func NewGroupWithContext(ctx context.Context, limit int, retryOpts ...retry.Option) (*Group, context.Context) {
	ctx, cancel := context.WithCancelCause(ctx)
	return (&Group{cancel: cancel, ctx: ctx, opts: append(retryOpts, retry.Context(ctx))}).SetLimit(limit), ctx
}

// OrderedGroup
// 使得Lifecycle.Before是有序且线程安全
func NewOrderedGroupWithContext(ctx context.Context, limit int, retryOpts ...retry.Option) (*Group, context.Context) {
	group, ctx := NewGroupWithContext(ctx, limit, retryOpts...)
	group.startChan = make(chan token, 1)
	return group, ctx
}

func (g *Group) done() {
	if g.sem != nil {
		<-g.sem
	}
	g.wg.Done()
	atomic.AddUint64(&g.success, 1)
}

func (g *Group) Wait() error {
	g.wg.Wait()
	return context.Cause(g.ctx)
}

func (g *Group) Go(do func(ctx context.Context) error) {
	g.GoWithLifecycle(Lifecycle{Do: do})
}

type Lifecycle struct {
	// Before在OrderedGroup是有序且线程安全的
	// 只会被调用一次
	Before func(ctx context.Context) (err error)
	// 如果Before返回err就不调用Do
	Do func(ctx context.Context) (err error)
	// 最后调用一次After
	After func(err error)
}

func (g *Group) GoWithLifecycle(lifecycle Lifecycle) {
	if g.startChan != nil {
		select {
		case <-g.ctx.Done():
			return
		case g.startChan <- token{}:
		}
	}

	if g.sem != nil {
		select {
		case <-g.ctx.Done():
			return
		case g.sem <- token{}:
		}
	}

	g.wg.Add(1)
	go func() {
		defer g.done()
		var err error
		if lifecycle.Before != nil {
			err = lifecycle.Before(g.ctx)
		}
		if err == nil {
			if g.startChan != nil {
				<-g.startChan
			}
			err = retry.Do(func() error { return lifecycle.Do(g.ctx) }, g.opts...)
		}
		if lifecycle.After != nil {
			lifecycle.After(err)
		}
		if err != nil {
			select {
			case <-g.ctx.Done():
				return
			default:
				g.cancel(err)
			}
		}
	}()

}

func (g *Group) TryGo(f func(ctx context.Context) error) bool {
	if g.sem != nil {
		select {
		case g.sem <- token{}:
		default:
			return false
		}
	}

	g.wg.Add(1)
	go func() {
		defer g.done()
		if err := retry.Do(func() error { return f(g.ctx) }, g.opts...); err != nil {
			g.cancel(err)
		}
	}()
	return true
}

func (g *Group) SetLimit(n int) *Group {
	if len(g.sem) != 0 {
		panic(fmt.Errorf("errgroup: modify limit while %v goroutines in the group are still active", len(g.sem)))
	}
	if n > 0 {
		g.sem = make(chan token, n)
	} else {
		g.sem = nil
	}
	return g
}

func (g *Group) Success() uint64 {
	return atomic.LoadUint64(&g.success)
}

func (g *Group) Err() error {
	return context.Cause(g.ctx)
}
