package alist_v3

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	openlistdriver "github.com/OpenListTeam/OpenList/v4/drivers/openlist"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type familyDriver interface {
	List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error)
	ArchiveDecompress(context.Context, model.Obj, model.Obj, model.ArchiveDecompressArgs) error
}

func useTestClient(t *testing.T) {
	t.Helper()
	prev := base.RestyClient
	base.RestyClient = resty.New().SetTimeout(5 * time.Second)
	t.Cleanup(func() { base.RestyClient = prev })
}

// This descends two levels the way op.Get does, feeding an object from one
// listing back into List. It is one behavioral contract for both products.
func TestFamilyListSetsChildPaths(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 200, "message": "success",
			"data": map[string]any{"content": content, "total": len(content)},
		})
	}))
	t.Cleanup(srv.Close)
	useTestClient(t)

	drivers := map[string]familyDriver{
		"alist-v3": &AListV3{Addition: Addition{
			RootPath: driver.RootPath{RootFolderPath: "/drive"},
			Address:  srv.URL,
		}},
		"openlist": &openlistdriver.OpenList{Addition: openlistdriver.Addition{
			RootPath: driver.RootPath{RootFolderPath: "/drive"},
			Address:  srv.URL,
		}},
	}
	for name, d := range drivers {
		t.Run(name, func(t *testing.T) {
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
		})
	}
}

func TestFamilyDialectRequests(t *testing.T) {
	tests := []struct {
		name             string
		newDriver        func(string) familyDriver
		wantRefresh      bool
		wantOverwrite    bool
		wantOverwriteKey bool
	}{
		{
			name: "alist-v3 keeps legacy request shape",
			newDriver: func(address string) familyDriver {
				return &AListV3{Addition: Addition{
					Address: address, ForwardArchiveReq: true,
				}}
			},
		},
		{
			name: "openlist suppresses refresh unless enabled",
			newDriver: func(address string) familyDriver {
				return &openlistdriver.OpenList{Addition: openlistdriver.Addition{
					Address: address, ForwardArchiveReq: true,
				}}
			},
			wantOverwrite: true, wantOverwriteKey: true,
		},
		{
			name: "openlist forwards enabled flags",
			newDriver: func(address string) familyDriver {
				return &openlistdriver.OpenList{Addition: openlistdriver.Addition{
					Address: address, ForwardArchiveReq: true, PassRefreshFlagToUpsteam: true,
				}}
			},
			wantRefresh: true, wantOverwrite: true, wantOverwriteKey: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var listBody, decompressBody map[string]json.RawMessage
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/fs/list":
					_ = json.NewDecoder(r.Body).Decode(&listBody)
				case "/api/fs/archive/decompress":
					_ = json.NewDecoder(r.Body).Decode(&decompressBody)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"code":200,"message":"success","data":{"content":[]}}`))
			}))
			t.Cleanup(srv.Close)
			useTestClient(t)

			d := tt.newDriver(srv.URL)
			dir := &model.Object{Path: "/dst", IsFolder: true}
			if _, err := d.List(context.Background(), dir, model.ListArgs{Refresh: true}); err != nil {
				t.Fatal(err)
			}
			if err := d.ArchiveDecompress(
				context.Background(),
				&model.Object{Path: "/src/a.zip"},
				dir,
				model.ArchiveDecompressArgs{Overwrite: true},
			); err != nil {
				t.Fatal(err)
			}

			var gotRefresh bool
			if err := json.Unmarshal(listBody["refresh"], &gotRefresh); err != nil {
				t.Fatal(err)
			}
			if gotRefresh != tt.wantRefresh {
				t.Fatalf("refresh = %v, want %v", gotRefresh, tt.wantRefresh)
			}
			raw, hasOverwrite := decompressBody["overwrite"]
			if hasOverwrite != tt.wantOverwriteKey {
				t.Fatalf("overwrite presence = %v, want %v", hasOverwrite, tt.wantOverwriteKey)
			}
			if hasOverwrite {
				var gotOverwrite bool
				if err := json.Unmarshal(raw, &gotOverwrite); err != nil {
					t.Fatal(err)
				}
				if gotOverwrite != tt.wantOverwrite {
					t.Fatalf("overwrite = %v, want %v", gotOverwrite, tt.wantOverwrite)
				}
			}
		})
	}
}

func TestRoleDialectDecoding(t *testing.T) {
	var legacy MeResp
	if err := json.Unmarshal([]byte(`{"role":[2]}`), &legacy); err != nil {
		t.Fatalf("AList V3 array role: %v", err)
	}
	if len(legacy.Role) != 1 || legacy.Role[0] != 2 {
		t.Fatalf("AList V3 role = %v, want [2]", legacy.Role)
	}
	if err := json.Unmarshal([]byte(`{"role":2}`), &legacy); err != nil {
		t.Fatalf("AList V3 scalar role: %v", err)
	}

	var current openlistdriver.MeResp
	if err := json.Unmarshal([]byte(`{"role":2}`), &current); err != nil {
		t.Fatalf("OpenList scalar role: %v", err)
	}
	if err := json.Unmarshal([]byte(`{"role":[2]}`), &current); err == nil {
		t.Fatal("OpenList unexpectedly accepted an array role")
	}
}
