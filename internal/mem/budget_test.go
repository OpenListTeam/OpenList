package mem

import (
	"math"
	"sync"
	"testing"
)

func TestBudgetReservationLifecycle(t *testing.T) {
	budget := NewBudget(10)
	reservation, ok := budget.Reserve(6)
	if !ok {
		t.Fatal("Reserve(6) failed")
	}
	if budget.Reserved() != 6 || budget.Available() != 4 {
		t.Fatalf("budget after reserve = reserved:%d available:%d", budget.Reserved(), budget.Available())
	}
	if reservation.Resize(11) {
		t.Fatal("Resize(11) succeeded beyond capacity")
	}
	if !reservation.Resize(8) || budget.Reserved() != 8 {
		t.Fatalf("budget after grow = reserved:%d", budget.Reserved())
	}
	if !reservation.Resize(3) || budget.Reserved() != 3 {
		t.Fatalf("budget after shrink = reserved:%d", budget.Reserved())
	}
	reservation.Release()
	reservation.Release()
	if budget.Reserved() != 0 || budget.Available() != 10 {
		t.Fatalf("budget after release = reserved:%d available:%d", budget.Reserved(), budget.Available())
	}
	if reservation.Resize(1) {
		t.Fatal("Resize() succeeded after release")
	}
}

func TestBudgetConcurrentReservationsDoNotOversubscribe(t *testing.T) {
	const (
		capacity = 32
		claim    = 4
		workers  = 64
	)
	budget := NewBudget(capacity)
	start := make(chan struct{})
	results := make(chan *Reservation, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			reservation, ok := budget.Reserve(claim)
			if !ok {
				results <- nil
				return
			}
			results <- reservation
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var reservations []*Reservation
	for reservation := range results {
		if reservation != nil {
			reservations = append(reservations, reservation)
		}
	}
	if len(reservations) != capacity/claim {
		t.Fatalf("successful reservations = %d, want %d", len(reservations), capacity/claim)
	}
	if budget.Reserved() != capacity || budget.Available() != 0 {
		t.Fatalf("full budget = reserved:%d available:%d", budget.Reserved(), budget.Available())
	}
	for _, reservation := range reservations {
		reservation.Release()
	}
	if budget.Reserved() != 0 {
		t.Fatalf("reserved after releases = %d", budget.Reserved())
	}
}

func TestBudgetBoundaries(t *testing.T) {
	budget := NewBudget(math.MaxUint64)
	reservation, ok := budget.Reserve(math.MaxUint64 - 5)
	if !ok {
		t.Fatal("large reservation failed")
	}
	if _, ok := budget.Reserve(6); ok {
		t.Fatal("overflowing reservation succeeded")
	}
	if budget.Available() != 5 {
		t.Fatalf("available = %d, want 5", budget.Available())
	}
	budget.SetCapacity(1)
	if budget.Available() != 0 {
		t.Fatalf("available below existing reservations = %d, want 0", budget.Available())
	}
	reservation.Release()
	if zero, ok := budget.Reserve(0); !ok {
		t.Fatal("zero reservation failed")
	} else {
		zero.Release()
	}
}
