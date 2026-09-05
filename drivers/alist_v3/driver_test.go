package alist_v3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

// TestListSetsChildPaths descends two levels the way op.Get does, feeding an
// object from one listing back into List as dir. Without a Path on that object
// the driver asks upstream for "", which a real server answers with its own
// root -- hence the fake upstream's fallback, and the endless self-similar tree.
func TestListSetsChildPaths(t *testing.T) {
	tree := map[string][]ObjResp{
		"/":               {{Name: "root-marker", IsDir: true}},
		"/drive":          {{Name: "concerts", IsDir: true}},
		"/drive/concerts": {{Name: "show.mkv"}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ListReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		content, ok := tree[req.Path]
		if !ok {
			content = tree["/"]
		}
		w.Header().Set("Content-Type", "application/json") // resty only unmarshals JSON
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": len(content)},
		})
	}))
	t.Cleanup(srv.Close)

	// conf.Conf is nil outside a booted server, so base.InitClient() is unusable.
	prev := base.RestyClient
	base.RestyClient = resty.New().SetTimeout(5 * time.Second)
	t.Cleanup(func() { base.RestyClient = prev })

	d := &AListV3{Addition: Addition{
		RootPath: driver.RootPath{RootFolderPath: "/drive"},
		Address:  srv.URL,
	}}
	dir := model.Obj(&model.Object{Path: "/drive", IsFolder: true})
	for _, want := range []string{"/drive/concerts", "/drive/concerts/show.mkv"} {
		objs, err := d.List(context.Background(), dir, model.ListArgs{})
		if err != nil {
			t.Fatalf("List(%q): %v", dir.GetPath(), err)
		}
		if len(objs) != 1 {
			t.Fatalf("List(%q) returned %d objects, want 1", dir.GetPath(), len(objs))
		}
		if got := objs[0].GetPath(); got != want {
			t.Fatalf("child of %q has path %q, want %q", dir.GetPath(), got, want)
		}
		dir = objs[0]
	}
}
