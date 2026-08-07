package teldrive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

// An empty directory makes the Teldrive API report totalPages == 0. List used
// to size pagesData by that value and then write pagesData[0], which panicked
// with "index out of range [0] with length 0" on every empty folder.
func TestListEmptyDir(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[],"meta":{"count":0,"totalPages":0,"currentPage":1}}`))
	}))
	defer srv.Close()

	oldClient := base.RestyClient
	base.RestyClient = resty.New()
	defer func() { base.RestyClient = oldClient }()

	d := &Teldrive{}
	d.Address = srv.URL

	objs, err := d.List(context.Background(), &model.Object{Path: "/"}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}
	if len(objs) != 0 {
		t.Fatalf("expected no entries for an empty dir, got %d", len(objs))
	}
}
