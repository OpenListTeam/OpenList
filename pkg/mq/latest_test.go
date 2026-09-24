package mq

import (
	"context"
	"testing"
	"time"
)

func newTestLatestProcessor[K comparable, V any](maxKeys, maxWeight int) *LatestProcessor[K, V] {
	return &LatestProcessor[K, V]{
		entries: make(map[K]latestEntry[V]), inFlight: make(map[K]int),
		ready:   make(chan struct{}, 1),
		maxKeys: maxKeys, maxWeight: maxWeight,
	}
}

func TestLatestProcessorCoalescesByKey(t *testing.T) {
	mailbox := newTestLatestProcessor[string, string](2, 4)
	if got := mailbox.Offer("/a", "old", 2); got != OfferAdded {
		t.Fatalf("first offer = %v, want added", got)
	}
	if got := mailbox.Offer("/a", "new", 3); got != OfferReplaced {
		t.Fatalf("replacement = %v, want replaced", got)
	}
	if got := mailbox.Offer("/b", "other", 1); got != OfferAdded {
		t.Fatalf("second key = %v, want added", got)
	}

	key, value, ok := mailbox.take(context.Background())
	if !ok || key != "/a" || value != "new" {
		t.Fatalf("first take = (%q, %q, %t), want latest /a", key, value, ok)
	}
	mailbox.done(key)
	stats := mailbox.Stats()
	if stats.Replaced != 1 || stats.Pending != 1 || stats.Weight != 1 {
		t.Fatalf("stats after coalescing = %+v", stats)
	}
}

func TestLatestProcessorRejectsCapacityWithoutDiscardingPendingValue(t *testing.T) {
	mailbox := newTestLatestProcessor[string, string](1, 2)
	mailbox.Offer("/a", "kept", 2)
	if got := mailbox.Offer("/b", "other", 1); got != OfferRejectedCapacity {
		t.Fatalf("new key over capacity = %v, want rejected", got)
	}
	if got := mailbox.Offer("/a", "too-large", 3); got != OfferRejectedCapacity {
		t.Fatalf("replacement over capacity = %v, want rejected", got)
	}
	key, value, ok := mailbox.take(context.Background())
	if !ok || key != "/a" || value != "kept" {
		t.Fatalf("take after rejection = (%q, %q, %t), want retained value", key, value, ok)
	}
	mailbox.done(key)
	if stats := mailbox.Stats(); stats.Rejected != 2 || stats.Pending != 0 || stats.Weight != 0 {
		t.Fatalf("stats after rejection = %+v", stats)
	}
}

func TestLatestProcessorCloseRejectsNewWorkAndDrainsPending(t *testing.T) {
	mailbox := newTestLatestProcessor[string, string](1, 1)
	mailbox.Offer("/a", "pending", 1)
	mailbox.stopped = true
	if got := mailbox.Offer("/b", "rejected", 1); got != OfferRejectedClosed {
		t.Fatalf("offer after close = %v, want closed rejection", got)
	}
	key, value, ok := mailbox.take(context.Background())
	if !ok || value != "pending" {
		t.Fatalf("pending value was not drained: (%q, %t)", value, ok)
	}
	mailbox.done(key)
	if _, _, ok := mailbox.take(context.Background()); ok {
		t.Fatal("closed drained mailbox returned another value")
	}
}

func TestLatestProcessorSerializesOneKeyAcrossWorkers(t *testing.T) {
	mailbox := newTestLatestProcessor[string, string](2, 4)
	mailbox.Offer("/a", "first", 1)
	key, _, ok := mailbox.take(context.Background())
	if !ok {
		t.Fatal("first take failed")
	}
	mailbox.Offer("/a", "second", 1)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, ok := mailbox.take(ctx); ok {
		t.Fatal("same key was taken while its first value was in flight")
	}
	mailbox.done(key)
	if _, value, ok := mailbox.take(context.Background()); !ok || value != "second" {
		t.Fatalf("latest same-key value after completion = (%q, %t)", value, ok)
	}
}

func TestLatestProcessorStopDrainsAcceptedWork(t *testing.T) {
	processed := make(chan string, 1)
	processor := NewLatestProcessor(1, 1, 1, func(_ context.Context, _ string, value string) {
		processed <- value
	})
	processor.Offer("/a", "accepted", 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := processor.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if got := <-processed; got != "accepted" {
		t.Fatalf("processed %q, want accepted", got)
	}
}

func BenchmarkLatestProcessorReplace(b *testing.B) {
	mailbox := newTestLatestProcessor[string, int](1, 1)
	for i := 0; b.Loop(); i++ {
		mailbox.Offer("parent", i, 1)
	}
}

func BenchmarkLatestProcessorAdmissionRoundTrip(b *testing.B) {
	mailbox := newTestLatestProcessor[int, int](1, 1)
	for i := 0; b.Loop(); i++ {
		mailbox.Offer(i, i, 1)
		key, _, _ := mailbox.take(context.Background())
		mailbox.done(key)
	}
}
