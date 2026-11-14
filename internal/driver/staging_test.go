package driver

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// Names are never trusted on their looks: only a tag this server computed for
// this very storage makes one.
func TestStagingCreatedRejectsOtherNames(t *testing.T) {
	st := &model.Storage{ID: 7}
	valid := StagingNameAt(st, time.Unix(1_800_000_000, 0))
	forged := valid[:len(valid)-1] + "0"
	if forged == valid {
		forged = valid[:len(valid)-1] + "1"
	}
	names := []string{
		".openlist-upload-0123456789abcdef", // what a user might name a file
		".openlist-upload-ABCDEFGHabcdefgh",
		".openlist-upload-notes.txt",
		StagingNameAt(&model.Storage{ID: 8}, time.Now()), // another storage's, e.g. one layered on this
		forged,
		valid + "0",
		strings.TrimPrefix(valid, "."),
		"report.pdf",
	}
	if upper := valid[:len(valid)-16] + strings.ToUpper(valid[len(valid)-16:]); upper != valid {
		names = append(names, upper)
	}
	for _, name := range names {
		if _, ok := StagingCreated(st, name); ok {
			t.Errorf("StagingCreated(%q) = true, want a name this storage never staged", name)
		}
	}
}

// One key at a time, and names differing only in case are one key, as they
// are one file on many storages: a waiter gives up once its context ends, and
// a key leaves nothing behind once its last holder let go.
func TestKeyedMutex(t *testing.T) {
	var k KeyedMutex
	unlock, err := k.Lock(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	unlockB, err := k.Lock(context.Background(), "b")
	if err != nil {
		t.Fatalf("another key was held up: %v", err)
	}
	unlockB()
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan error, 1)
	go func() {
		_, err := k.Lock(ctx, "A")
		waited <- err
	}()
	select {
	case err := <-waited:
		t.Fatalf("Lock of a held key returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	if err := <-waited; !errors.Is(err, context.Canceled) {
		t.Fatalf("a canceled waiter returned %v, want context.Canceled", err)
	}
	if _, err := k.Lock(ctx, "c"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Lock with a canceled context returned %v, want context.Canceled", err)
	}
	unlock()
	unlock, err = k.Lock(context.Background(), "a")
	if err != nil {
		t.Fatalf("the released key is still taken: %v", err)
	}
	unlock()
	if n := len(k.locks); n != 0 {
		t.Fatalf("%d keys left behind", n)
	}
}
