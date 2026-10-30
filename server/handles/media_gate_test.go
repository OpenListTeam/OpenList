package handles

import (
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestMediaPathAdmission(t *testing.T) {
	user := &model.User{ID: 2, BasePath: "/media"}
	tests := []struct {
		name         string
		user         *model.User
		path         string
		meta         *model.Meta
		err          error
		want, lookup bool
	}{
		{"missing user", nil, "/media/a", nil, nil, false, false},
		{"disabled guest", &model.User{Role: model.GUEST, Disabled: true}, "/media/a", nil, nil, false, false},
		{"prefix sibling", user, "/media-other/a", nil, nil, false, false},
		{"normalized traversal", user, "/media/../other/a", nil, nil, false, false},
		{"missing meta", user, "/media/a", nil, errs.MetaNotFound, true, true},
		{"lookup failure", user, "/media/a", nil, errors.New("database unavailable"), false, true},
		{"restricted reader", user, "/media/a", &model.Meta{Path: "/media", ReadUsers: []uint{1}, ReadUsersSub: true}, nil, false, true},
		{"hidden child", user, "/media/a", &model.Meta{Path: "/media", Hide: "^a$"}, nil, false, true},
		{"password unavailable", user, "/media/a", &model.Meta{Path: "/media", Password: "secret", PSub: true}, nil, false, true},
		{"password exemption", &model.User{ID: 2, BasePath: "/media", Permission: 2}, "/media/a", &model.Meta{Path: "/media", Password: "secret", PSub: true}, nil, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			resolve := func(p string) (*model.Meta, error) {
				called = true
				if p != tt.path {
					t.Fatalf("meta path = %q, want %q", p, tt.path)
				}
				return tt.meta, tt.err
			}
			if got := canUserAccessPath(tt.user, tt.path, resolve); got != tt.want {
				t.Fatalf("admission = %v, want %v", got, tt.want)
			}
			if called != tt.lookup {
				t.Fatalf("meta lookup = %v, want %v", called, tt.lookup)
			}
		})
	}
}
