package pool

import "sync"

type Pool[T any] struct {
	New    func() T
	MaxCap int

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
	if p.MaxCap == 0 || len(p.cache) < int(p.MaxCap) {
		p.cache = append(p.cache, item)
	}
}

func (p *Pool[T]) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	clear(p.cache)
	p.cache = nil
}
