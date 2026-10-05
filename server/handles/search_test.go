package handles

import (
	"path"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func fakeResolveMeta(metas map[string]*model.Meta) func(string) (*model.Meta, error) {
	return func(p string) (*model.Meta, error) {
		for {
			if meta, ok := metas[p]; ok {
				return meta, nil
			}
			if p == "/" {
				return nil, errs.MetaNotFound
			}
			p = path.Dir(p)
		}
	}
}

func TestIsSearchNodeAccessible(t *testing.T) {
	tests := []struct {
		name         string
		metas        map[string]*model.Meta
		node         model.SearchNode
		want         bool
		wantMetaPath string
	}{
		{
			name:         "restricted directory",
			metas:        map[string]*model.Meta{"/private": {Path: "/private", ReadUsers: []uint{1}}},
			node:         model.SearchNode{Parent: "/", Name: "private", IsDir: true},
			want:         false,
			wantMetaPath: "/private",
		},
		{
			name:         "restricted sub directory",
			metas:        map[string]*model.Meta{"/private": {Path: "/private", ReadUsers: []uint{1}, ReadUsersSub: true}},
			node:         model.SearchNode{Parent: "/private", Name: "sub", IsDir: true},
			want:         false,
			wantMetaPath: "/private/sub",
		},
		{
			name:         "file keeps parent scope",
			metas:        map[string]*model.Meta{"/private": {Path: "/private", ReadUsers: []uint{1}}},
			node:         model.SearchNode{Parent: "/private", Name: "a.txt", IsDir: false},
			want:         true,
			wantMetaPath: "/private",
		},
		{
			name:         "outside base path",
			node:         model.SearchNode{Parent: "/other", Name: "private", IsDir: true},
			want:         false,
			wantMetaPath: "",
		},
	}
	user := &model.User{ID: 2, BasePath: "/"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolve := fakeResolveMeta(tt.metas)
			var gotMetaPath string
			spy := func(p string) (*model.Meta, error) {
				gotMetaPath = p
				return resolve(p)
			}
			if got := isSearchNodeAccessible(user, tt.node, "", spy); got != tt.want {
				t.Fatalf("isSearchNodeAccessible() = %v, want %v", got, tt.want)
			}
			if tt.wantMetaPath != "" && gotMetaPath != tt.wantMetaPath {
				t.Fatalf("meta resolved at %q, want %q", gotMetaPath, tt.wantMetaPath)
			}
		})
	}
}
