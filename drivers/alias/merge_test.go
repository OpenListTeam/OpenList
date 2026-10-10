package alias

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func objAt(dirPath, name string, size int64) model.Obj {
	return &model.Object{
		Name:     name,
		Path:     dirPath + "/" + name,
		Size:     size,
		IsFolder: false,
	}
}

func namesOf(objs []model.Obj) []string {
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	slices.Sort(names)
	return names
}

func TestMergeListsFirstBackendWins(t *testing.T) {
	lists := []listResult{
		{dirPath: "/mf100", objs: []model.Obj{
			objAt("/mf100", "shared.txt", 100),
			objAt("/mf100", "only-a.txt", 1),
		}},
		{dirPath: "/mf102", objs: []model.Obj{
			objAt("/mf102", "shared.txt", 200),
			objAt("/mf102", "only-b.txt", 2),
		}},
		{dirPath: "/mf104", objs: nil},
	}

	merged := mergeLists(lists)
	got := namesOf(merged)
	want := []string{"only-a.txt", "only-b.txt", "shared.txt"}
	if !slices.Equal(got, want) {
		t.Fatalf("unexpected merge result: %v, want %v", got, want)
	}
	for _, o := range merged {
		if o.GetName() == "shared.txt" {
			if o.GetSize() != 100 {
				t.Fatalf("expected first backend copy to win, got size %d", o.GetSize())
			}
			if o.GetPath() != "/mf100/shared.txt" {
				t.Fatalf("expected path from first backend, got %s", o.GetPath())
			}
		}
	}
}

func TestFanOutPreservesOrder(t *testing.T) {
	items := make([]int, 64)
	for i := range items {
		items[i] = i
	}
	// Make later items finish first to prove ordering does not depend on
	// completion order.
	results := fanOut(context.Background(), 8, items, func(_ context.Context, i int) string {
		time.Sleep(time.Duration(64-i) * time.Millisecond)
		return fmt.Sprintf("item-%d", i)
	})
	for i, r := range results {
		if r != fmt.Sprintf("item-%d", i) {
			t.Fatalf("result at index %d is %q", i, r)
		}
	}
}

func TestFanOutRespectsConcurrency(t *testing.T) {
	const concurrency = 4
	running := 0
	peak := 0
	var mu = make(chan struct{}, 1)
	items := make([]int, 32)
	fanOut(context.Background(), concurrency, items, func(_ context.Context, _ int) struct{} {
		mu <- struct{}{}
		running++
		if running > peak {
			peak = running
		}
		running--
		<-mu
		time.Sleep(5 * time.Millisecond)
		return struct{}{}
	})
	if peak > concurrency {
		t.Fatalf("observed %d concurrent calls, limit was %d", peak, concurrency)
	}
}
