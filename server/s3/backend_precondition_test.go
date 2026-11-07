package s3

import (
	"context"
	"github.com/OpenListTeam/gofakes3"
	"io"
	"strings"
	"testing"
)

func TestPutPreconditionsRunInsideKeyLock(t *testing.T) {
	b, _ := setupMultipartBackend(t)
	ctx := context.Background()
	if _, err := b.PutObject(ctx, "mp", "conditional.txt", nil, strings.NewReader("old"), 3); err != nil {
		t.Fatal(err)
	}
	star := context.WithValue(ctx, putPreconditionsContextKey{}, putPreconditions{ifNoneMatch: "*"})
	if _, err := b.PutObject(star, "mp", "conditional.txt", nil, strings.NewReader("new"), 3); err != gofakes3.ErrPreconditionFailed {
		t.Fatalf("overwrite with If-None-Match:*: %v", err)
	}
	got, err := b.GetObject(ctx, "mp", "conditional.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Contents)
	got.Contents.Close()
	if string(body) != "old" {
		t.Fatalf("failed condition changed object: %q", body)
	}

	bad := context.WithValue(ctx, putPreconditionsContextKey{}, putPreconditions{ifMatch: `"not-current"`})
	if _, err := b.PutObject(bad, "mp", "conditional.txt", nil, strings.NewReader("new"), 3); err != gofakes3.ErrPreconditionFailed {
		t.Fatalf("stale If-Match: %v", err)
	}
	fresh := context.WithValue(ctx, putPreconditionsContextKey{}, putPreconditions{ifMatch: `"` + got.ETag + `"`})
	if _, err := b.PutObject(fresh, "mp", "conditional.txt", nil, strings.NewReader("new"), 3); err != nil {
		t.Fatalf("matching If-Match: %v", err)
	}
}
