package mem

import (
	"runtime"
	"sync"
)

// CacheMemoryBudget accounts for memory reserved by active HybridCache instances.
// Bootstrap configures its capacity from the effective available memory.
var CacheMemoryBudget = NewBudget(0)

// Budget is a process-local capacity shared by cache reservations.
type Budget struct {
	mu       sync.Mutex
	capacity uint64
	reserved uint64
}

// NewBudget creates an empty budget with the given capacity.
func NewBudget(capacity uint64) *Budget {
	return &Budget{capacity: capacity}
}

// SetCapacity changes the maximum number of bytes that reservations may hold.
// Existing reservations remain valid if the capacity is reduced below them.
func (b *Budget) SetCapacity(capacity uint64) {
	b.mu.Lock()
	b.capacity = capacity
	b.mu.Unlock()
}

// Capacity returns the maximum number of bytes the budget can reserve.
func (b *Budget) Capacity() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.capacity
}

// Reserved returns the number of bytes held by active reservations.
func (b *Budget) Reserved() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reserved
}

// Available returns the number of bytes that remain reservable.
func (b *Budget) Available() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.reserved >= b.capacity {
		return 0
	}
	return b.capacity - b.reserved
}

// Reserve atomically claims size bytes until the returned reservation is
// released. A zero-sized reservation may be grown later.
func (b *Budget) Reserve(size uint64) (*Reservation, bool) {
	if !b.resize(0, size) {
		return nil, false
	}
	state := &reservationState{budget: b, size: size}
	reservation := &Reservation{state: state}
	reservation.cleanup = runtime.AddCleanup(reservation, releaseReservation, state)
	return reservation, true
}

func (b *Budget) release(size uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size > b.reserved {
		panic("cache memory budget reservation underflow")
	}
	b.reserved -= size
}

func (b *Budget) resize(current, target uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if current > b.reserved {
		panic("cache memory budget reservation exceeds total reserved memory")
	}
	if target > current {
		growth := target - current
		if b.reserved > b.capacity || growth > b.capacity-b.reserved {
			return false
		}
		b.reserved += growth
	} else {
		b.reserved -= current - target
	}
	return true
}

type reservationState struct {
	mu       sync.Mutex
	budget   *Budget
	size     uint64
	released bool
}

func releaseReservation(state *reservationState) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return
	}
	state.budget.release(state.size)
	state.size = 0
	state.released = true
}

// Reservation is an idempotently releasable claim on a Budget.
type Reservation struct {
	state   *reservationState
	cleanup runtime.Cleanup
}

// Resize changes the reservation while preserving atomic budget accounting.
// It returns false when growing would exceed the budget or after Release.
func (r *Reservation) Resize(size uint64) bool {
	if r == nil || r.state == nil {
		return false
	}
	state := r.state
	defer runtime.KeepAlive(r)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.released {
		return false
	}
	current := state.size
	if !state.budget.resize(current, size) {
		return false
	}
	state.size = size
	return true
}

// Size returns the number of bytes currently held by the reservation.
func (r *Reservation) Size() uint64 {
	if r == nil || r.state == nil {
		return 0
	}
	state := r.state
	defer runtime.KeepAlive(r)
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.size
}

// Release returns the reserved capacity to the budget. It is idempotent.
func (r *Reservation) Release() {
	if r == nil || r.state == nil {
		return
	}
	defer runtime.KeepAlive(r)
	r.cleanup.Stop()
	releaseReservation(r.state)
}
