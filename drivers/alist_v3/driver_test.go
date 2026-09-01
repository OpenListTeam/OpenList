package alist_v3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

// useTestClient installs a resty client that does not read conf.Conf, which is
// nil outside of a booted server.
func useTestClient(t *testing.T) {
	t.Helper()
	prev := base.RestyClient
	base.RestyClient = resty.New().SetTimeout(5 * time.Second)
	t.Cleanup(func() { base.RestyClient = prev })
}

// recorder collects the paths a fake upstream was asked for.
type recorder struct {
	mu    sync.Mutex
	paths []string
}

func (r *recorder) add(p string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paths = append(r.paths, p)
}

func (r *recorder) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.paths...)
}

// fakeUpstream serves /api/fs/list from a fixed tree.
//
// The important detail is the fallback: an OpenList server resolves an empty
// path to its own root, so a request that lost its sub-path does not fail --
// it silently returns the wrong directory.
func fakeUpstream(t *testing.T, tree map[string][]ObjResp) (string, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ListReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("undecodable request body: %v", err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rec.add(req.Path)
		content, ok := tree[req.Path]
		if !ok {
			content = tree["/"]
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"code":    200,
			"message": "success",
			"data": map[string]any{
				"content": content,
				"total":   len(content),
			},
		}); err != nil {
			t.Errorf("failed to encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL, rec
}

func newTestDriver(address, rootFolderPath string) *AListV3 {
	return &AListV3{
		Addition: Addition{
			RootPath: driver.RootPath{RootFolderPath: rootFolderPath},
			Address:  address,
			Token:    "test-token",
		},
	}
}

func names(objs []model.Obj) []string {
	out := make([]string, 0, len(objs))
	for _, o := range objs {
		out = append(out, o.GetName())
	}
	return out
}

// TestListSetsChildPaths pins the contract op.Get relies on: it returns the
// child object from its parent's listing verbatim, so whatever Path that object
// carries is what the next List sends upstream.
func TestListSetsChildPaths(t *testing.T) {
	useTestClient(t)

	address, _ := fakeUpstream(t, map[string][]ObjResp{
		"/":      {{Name: "root-marker", IsDir: true}},
		"/drive": {{Name: "concerts", IsDir: true}, {Name: "movie.mkv"}},
	})
	d := newTestDriver(address, "/drive")

	objs, err := d.List(context.Background(),
		&model.Object{Path: d.RootFolderPath, Name: "root", IsFolder: true},
		model.ListArgs{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("got %d objects, want 2: %v", len(objs), names(objs))
	}
	for _, obj := range objs {
		want := "/drive/" + obj.GetName()
		if got := obj.GetPath(); got != want {
			t.Errorf("child %q has path %q, want %q", obj.GetName(), got, want)
		}
	}
}

// TestListDoesNotLoopOnNestedDirectories is the regression proper. A child
// without a Path makes the driver ask the upstream server for "", the server
// answers with its own root, and every level below the mount point serves that
// same listing back -- an endless self-similar directory.
func TestListDoesNotLoopOnNestedDirectories(t *testing.T) {
	useTestClient(t)

	address, rec := fakeUpstream(t, map[string][]ObjResp{
		"/":                             {{Name: "drive", IsDir: true}, {Name: "public", IsDir: true}},
		"/drive":                        {{Name: "concerts", IsDir: true}},
		"/drive/concerts":               {{Name: "live-in-tokyo", IsDir: true}},
		"/drive/concerts/live-in-tokyo": {{Name: "show.mkv"}},
	})
	d := newTestDriver(address, "/drive")
	ctx := context.Background()

	dir := model.Obj(&model.Object{Path: d.RootFolderPath, Name: "root", IsFolder: true})
	for _, want := range []struct {
		requested string
		listing   []string
	}{
		{"/drive", []string{"concerts"}},
		{"/drive/concerts", []string{"live-in-tokyo"}},
		{"/drive/concerts/live-in-tokyo", []string{"show.mkv"}},
	} {
		objs, err := d.List(ctx, dir, model.ListArgs{})
		if err != nil {
			t.Fatalf("List(%q): %v", want.requested, err)
		}
		if got := names(objs); len(got) != 1 || got[0] != want.listing[0] {
			t.Fatalf("listing under %q is %v, want %v", want.requested, got, want.listing)
		}
		dir = objs[0]
	}

	got := rec.get()
	wantPaths := []string{"/drive", "/drive/concerts", "/drive/concerts/live-in-tokyo"}
	if len(got) != len(wantPaths) {
		t.Fatalf("upstream saw %v, want %v", got, wantPaths)
	}
	for i, want := range wantPaths {
		if got[i] != want {
			t.Errorf("request %d asked for %q, want %q", i, got[i], want)
		}
	}
}
