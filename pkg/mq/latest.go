package mq

import (
	"context"
	"sync"
)

type OfferResult uint8

const (
	OfferAdded OfferResult = iota
	OfferReplaced
	OfferRejectedCapacity
	OfferRejectedClosed
)

type MailboxStats struct {
	Replaced, Rejected        uint64
	Pending, InFlight, Weight int
}

type latestEntry[V any] struct {
	value  V
	weight int
}

// LatestProcessor runs fixed workers over a bounded latest-value-per-key queue.
type LatestProcessor[K comparable, V any] struct {
	mu                 sync.Mutex
	entries            map[K]latestEntry[V]
	inFlight           map[K]int
	order              []K
	ready, exited      chan struct{}
	maxKeys, maxWeight int
	weight             int
	stopped            bool
	stats              MailboxStats
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	process            func(context.Context, K, V)
}

func NewLatestProcessor[K comparable, V any](maxKeys, maxWeight, workers int, process func(context.Context, K, V)) *LatestProcessor[K, V] {
	if maxKeys < 1 || maxWeight < 0 || workers < 1 || process == nil {
		panic("mq: invalid latest processor")
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &LatestProcessor[K, V]{
		entries: make(map[K]latestEntry[V]), inFlight: make(map[K]int),
		ready: make(chan struct{}, 1), exited: make(chan struct{}),
		maxKeys: maxKeys, maxWeight: maxWeight, cancel: cancel, process: process,
	}
	for range workers {
		p.wg.Add(1)
		go p.run(ctx)
	}
	go func() { p.wg.Wait(); close(p.exited) }()
	return p
}

func (p *LatestProcessor[K, V]) Offer(key K, value V, weight int) OfferResult {
	if weight < 0 {
		panic("mq: negative latest processor weight")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		p.stats.Rejected++
		return OfferRejectedClosed
	}
	if entry, ok := p.entries[key]; ok {
		if next := p.weight - entry.weight + weight; next <= p.maxWeight {
			p.entries[key], p.weight = latestEntry[V]{value, weight}, next
			p.stats.Replaced++
			return OfferReplaced
		}
		p.stats.Rejected++
		return OfferRejectedCapacity
	}
	_, busy := p.inFlight[key]
	if (!busy && len(p.entries)+len(p.inFlight) == p.maxKeys) || p.weight+weight > p.maxWeight {
		p.stats.Rejected++
		return OfferRejectedCapacity
	}
	p.entries[key] = latestEntry[V]{value, weight}
	p.order, p.weight = append(p.order, key), p.weight+weight
	p.signal()
	return OfferAdded
}

func (p *LatestProcessor[K, V]) Stats() MailboxStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	stats := p.stats
	stats.Pending, stats.InFlight, stats.Weight = len(p.entries), len(p.inFlight), p.weight
	return stats
}

func (p *LatestProcessor[K, V]) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		p.signal()
	}
	p.mu.Unlock()
	select {
	case <-p.exited:
		p.cancel()
		return nil
	case <-ctx.Done():
		p.cancel()
		return ctx.Err()
	}
}

func (p *LatestProcessor[K, V]) run(ctx context.Context) {
	defer p.wg.Done()
	for {
		key, value, ok := p.take(ctx)
		if !ok {
			return
		}
		func() { defer p.done(key); p.process(ctx, key, value) }()
	}
}

func (p *LatestProcessor[K, V]) take(ctx context.Context) (K, V, bool) {
	for {
		p.mu.Lock()
		for i, key := range p.order {
			if _, busy := p.inFlight[key]; busy {
				continue
			}
			entry := p.entries[key]
			p.order = append(p.order[:i], p.order[i+1:]...)
			delete(p.entries, key)
			p.inFlight[key] = entry.weight
			if len(p.order) > 0 {
				p.signal()
			}
			p.mu.Unlock()
			return key, entry.value, true
		}
		if p.stopped && len(p.entries) == 0 {
			p.signal()
			p.mu.Unlock()
			return emptyLatest[K, V]()
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return emptyLatest[K, V]()
		case <-p.ready:
		}
	}
}

func emptyLatest[K comparable, V any]() (K, V, bool) {
	var key K
	var value V
	return key, value, false
}

func (p *LatestProcessor[K, V]) done(key K) {
	p.mu.Lock()
	weight, ok := p.inFlight[key]
	if ok {
		delete(p.inFlight, key)
		p.weight -= weight
	}
	p.mu.Unlock()
	if ok {
		p.signal()
	}
}

func (p *LatestProcessor[K, V]) signal() {
	select {
	case p.ready <- struct{}{}:
	default:
	}
}
