package pool

import "sync"

type Pool[T any] struct {
	New   func() T
	cache []T
	mu    sync.Mutex
}

func (p *Pool[T]) Get() T {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.cache) == 0 {
		return p.New()
	}
	item := p.cache[len(p.cache)-1]
	p.cache = p.cache[:len(p.cache)-1]
	return item
}

func (p *Pool[T]) Put(item T) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = append(p.cache, item)
}

func (p *Pool[T]) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.cache)
	p.cache = nil
}

func (p *Pool[T]) Close() error {
	p.Reset()
	return nil
}
