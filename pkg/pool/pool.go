package pool

// A simple object pool implementation. Not thread-safe.
type Pool[T any] struct {
	New   func() T
	cache []T
}

func (p *Pool[T]) Get() T {
	if len(p.cache) == 0 {
		return p.New()
	}
	item := p.cache[len(p.cache)-1]
	p.cache = p.cache[:len(p.cache)-1]
	return item
}

func (p *Pool[T]) Put(item T) {
	p.cache = append(p.cache, item)
}

func (p *Pool[T]) Reset() {
	clear(p.cache)
	p.cache = nil
}

func (p *Pool[T]) Close() error {
	p.Reset()
	return nil
}
