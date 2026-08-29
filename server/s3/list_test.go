package s3

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/gofakes3"
)

func TestListPageBoundsRecursiveTraversal(t *testing.T) {
	listCalls := 0
	b := &s3Backend{
		listDir: func(_ context.Context, dir string) ([]model.Obj, error) {
			listCalls++
			if dir == "bucket/data" {
				entries := make([]model.Obj, 256)
				for i := range entries {
					entries[i] = &model.Object{Name: fmt.Sprintf("%02x", i), IsFolder: true}
				}
				return entries, nil
			}
			return []model.Obj{&model.Object{Name: "pack", Size: 1}}, nil
		},
	}

	got, err := b.listPage(context.Background(), "bucket", "data", "", false, gofakes3.ListBucketPage{MaxKeys: 3})
	if err != nil {
		t.Fatalf("listPage() error = %v", err)
	}

	wantKeys := []string{"data/00/pack", "data/01/pack", "data/02/pack"}
	if len(got.Contents) != len(wantKeys) {
		t.Fatalf("len(Contents) = %d, want %d", len(got.Contents), len(wantKeys))
	}
	for i, want := range wantKeys {
		if got.Contents[i].Key != want {
			t.Errorf("Contents[%d].Key = %q, want %q", i, got.Contents[i].Key, want)
		}
	}
	if !got.IsTruncated {
		t.Error("IsTruncated = false, want true")
	}
	if got.NextMarker != wantKeys[len(wantKeys)-1] {
		t.Errorf("NextMarker = %q, want %q", got.NextMarker, wantKeys[len(wantKeys)-1])
	}
	if listCalls > 5 {
		t.Errorf("list calls = %d, want at most 5 for a three-key page", listCalls)
	}
}

func TestListPageContinuesWithoutGapsOrDuplicates(t *testing.T) {
	b := &s3Backend{
		listDir: func(_ context.Context, dir string) ([]model.Obj, error) {
			if dir == "bucket/data" {
				entries := make([]model.Obj, 16)
				for i := range entries {
					entries[i] = &model.Object{Name: fmt.Sprintf("%02x", 15-i), IsFolder: true}
				}
				return entries, nil
			}
			entries := make([]model.Obj, 20)
			for i := range entries {
				entries[i] = &model.Object{Name: fmt.Sprintf("pack-%02d", 19-i), Size: 1}
			}
			return entries, nil
		},
	}

	var gotKeys []string
	page := gofakes3.ListBucketPage{MaxKeys: 37}
	for pageNumber := 0; pageNumber < 10; pageNumber++ {
		got, err := b.listPage(context.Background(), "bucket", "data", "", false, page)
		if err != nil {
			t.Fatalf("listPage() page %d error = %v", pageNumber, err)
		}
		for _, item := range got.Contents {
			gotKeys = append(gotKeys, item.Key)
		}
		if !got.IsTruncated {
			break
		}
		page.Marker = got.NextMarker
		page.HasMarker = true
	}

	wantKeys := make([]string, 0, 16*20)
	for shard := 0; shard < 16; shard++ {
		for pack := 0; pack < 20; pack++ {
			wantKeys = append(wantKeys, fmt.Sprintf("data/%02x/pack-%02d", shard, pack))
		}
	}
	sort.Strings(wantKeys)
	if len(gotKeys) != len(wantKeys) {
		t.Fatalf("listed %d keys, want %d", len(gotKeys), len(wantKeys))
	}
	for i, want := range wantKeys {
		if gotKeys[i] != want {
			t.Fatalf("key %d = %q, want %q", i, gotKeys[i], want)
		}
	}
}

func TestListPagePaginatesContentsAndCommonPrefixesTogether(t *testing.T) {
	b := &s3Backend{
		listDir: func(_ context.Context, _ string) ([]model.Obj, error) {
			return []model.Obj{
				&model.Object{Name: "d", IsFolder: true},
				&model.Object{Name: "c", Size: 1},
				&model.Object{Name: "b", IsFolder: true},
				&model.Object{Name: "a", Size: 1},
			}, nil
		},
	}

	first, err := b.listPage(context.Background(), "bucket", "data", "", true, gofakes3.ListBucketPage{MaxKeys: 2})
	if err != nil {
		t.Fatalf("first listPage() error = %v", err)
	}
	if len(first.Contents) != 1 || first.Contents[0].Key != "data/a" {
		t.Fatalf("first contents = %#v, want data/a", first.Contents)
	}
	if len(first.CommonPrefixes) != 1 || first.CommonPrefixes[0].Prefix != "data/b" {
		t.Fatalf("first prefixes = %#v, want data/b", first.CommonPrefixes)
	}
	if !first.IsTruncated || first.NextMarker != "data/b" {
		t.Fatalf("first page truncated = %v, next marker = %q", first.IsTruncated, first.NextMarker)
	}

	second, err := b.listPage(context.Background(), "bucket", "data", "", true, gofakes3.ListBucketPage{
		Marker:    first.NextMarker,
		HasMarker: true,
		MaxKeys:   2,
	})
	if err != nil {
		t.Fatalf("second listPage() error = %v", err)
	}
	if len(second.Contents) != 1 || second.Contents[0].Key != "data/c" {
		t.Fatalf("second contents = %#v, want data/c", second.Contents)
	}
	if len(second.CommonPrefixes) != 1 || second.CommonPrefixes[0].Prefix != "data/d" {
		t.Fatalf("second prefixes = %#v, want data/d", second.CommonPrefixes)
	}
	if second.IsTruncated || second.NextMarker != "" {
		t.Fatalf("second page truncated = %v, next marker = %q", second.IsTruncated, second.NextMarker)
	}
}

func TestListPageSkipsCompletedSubtrees(t *testing.T) {
	var calls []string
	b := &s3Backend{
		listDir: func(_ context.Context, dir string) ([]model.Obj, error) {
			calls = append(calls, dir)
			if dir == "bucket/data" {
				return []model.Obj{
					&model.Object{Name: "c", IsFolder: true},
					&model.Object{Name: "b", IsFolder: true},
					&model.Object{Name: "a", IsFolder: true},
				}, nil
			}
			return []model.Obj{&model.Object{Name: "pack", Size: 1}}, nil
		},
	}

	got, err := b.listPage(context.Background(), "bucket", "data", "", false, gofakes3.ListBucketPage{
		Marker:    "data/b/pack",
		HasMarker: true,
		MaxKeys:   1,
	})
	if err != nil {
		t.Fatalf("listPage() error = %v", err)
	}
	if len(got.Contents) != 1 || got.Contents[0].Key != "data/c/pack" {
		t.Fatalf("contents = %#v, want data/c/pack", got.Contents)
	}
	for _, call := range calls {
		if call == "bucket/data/a" {
			t.Fatalf("completed subtree was listed: calls = %v", calls)
		}
	}
}

func TestListPageUsesS3MaximumAndProbesForTruncation(t *testing.T) {
	b := &s3Backend{
		listDir: func(_ context.Context, _ string) ([]model.Obj, error) {
			entries := make([]model.Obj, maxListPageKeys+1)
			for i := range entries {
				entries[i] = &model.Object{Name: fmt.Sprintf("pack-%04d", i), Size: 1}
			}
			return entries, nil
		},
	}

	got, err := b.listPage(context.Background(), "bucket", "data", "", false, gofakes3.ListBucketPage{MaxKeys: 5000})
	if err != nil {
		t.Fatalf("listPage() error = %v", err)
	}
	if len(got.Contents) != int(maxListPageKeys) {
		t.Fatalf("len(Contents) = %d, want %d", len(got.Contents), maxListPageKeys)
	}
	if !got.IsTruncated || got.NextMarker != "data/pack-0999" {
		t.Fatalf("truncated = %v, next marker = %q", got.IsTruncated, got.NextMarker)
	}
}

func TestListPageDoesNotReturnEmptyDirectoryMarkerForDifferentPrefix(t *testing.T) {
	b := &s3Backend{
		listDir: func(_ context.Context, _ string) ([]model.Obj, error) {
			return nil, nil
		},
	}

	got, err := b.listPage(context.Background(), "bucket", "empty", "not-the-marker", false, gofakes3.ListBucketPage{MaxKeys: 10})
	if err != nil {
		t.Fatalf("listPage() error = %v", err)
	}
	if len(got.Contents) != 0 {
		t.Fatalf("contents = %#v, want empty", got.Contents)
	}
}

func TestListPageHonorsCanceledContext(t *testing.T) {
	listCalls := 0
	b := &s3Backend{
		listDir: func(_ context.Context, _ string) ([]model.Obj, error) {
			listCalls++
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := b.listPage(ctx, "bucket", "data", "", false, gofakes3.ListBucketPage{MaxKeys: 10})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("listPage() error = %v, want context.Canceled", err)
	}
	if listCalls != 0 {
		t.Fatalf("list calls = %d, want 0", listCalls)
	}
}

func TestListPageStopsWhenDirectoryReadCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	b := &s3Backend{
		listDir: func(_ context.Context, _ string) ([]model.Obj, error) {
			cancel()
			return []model.Obj{&model.Object{Name: "pack", Size: 1}}, nil
		},
	}

	_, err := b.listPage(ctx, "bucket", "data", "", false, gofakes3.ListBucketPage{MaxKeys: 10})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("listPage() error = %v, want context.Canceled", err)
	}
}

func TestListPageDoesNotReorderDirectoryCacheEntries(t *testing.T) {
	entries := []model.Obj{
		&model.Object{Name: "c", Size: 1},
		&model.Object{Name: "a", Size: 1},
		&model.Object{Name: "b", Size: 1},
	}
	b := &s3Backend{
		listDir: func(_ context.Context, _ string) ([]model.Obj, error) {
			return entries, nil
		},
	}

	if _, err := b.listPage(context.Background(), "bucket", "data", "", false, gofakes3.ListBucketPage{MaxKeys: 10}); err != nil {
		t.Fatalf("listPage() error = %v", err)
	}
	wantOrder := []string{"c", "a", "b"}
	for i, want := range wantOrder {
		if entries[i].GetName() != want {
			t.Fatalf("entry %d = %q, want original order %q", i, entries[i].GetName(), want)
		}
	}
}
