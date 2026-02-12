package fs

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestWhetherHide(t *testing.T) {
	tests := []struct {
		name   string
		user   *model.User
		meta   *model.Meta
		path   string
		want   bool
		reason string
	}{
		{
			name: "nil user",
			user: nil,
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: true,
			},
			path: "/folder",
			want: false,
			reason: "nil user (treated as admin) should not hide",
		},
		{
			name: "user with can_see_hides permission",
			user: &model.User{
				Role:       model.GENERAL,
				Permission: 1, // bit 0 set = can see hides
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: true,
			},
			path: "/folder",
			want: false,
			reason: "user with can_see_hides permission should not hide",
		},
		{
			name: "nil meta",
			user: &model.User{
				Role: model.GUEST,
			},
			meta: nil,
			path: "/folder",
			want: false,
			reason: "nil meta should not hide",
		},
		{
			name: "empty hide string",
			user: &model.User{
				Role: model.GUEST,
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "",
				HSub: true,
			},
			path: "/folder",
			want: false,
			reason: "empty hide string should not hide",
		},
		{
			name: "exact path match with HSub=false",
			user: &model.User{
				Role: model.GUEST,
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: false,
			},
			path: "/folder",
			want: true,
			reason: "exact path match should hide for guest",
		},
		{
			name: "sub path with HSub=true",
			user: &model.User{
				Role: model.GUEST,
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: true,
			},
			path: "/folder/subfolder",
			want: true,
			reason: "sub path with HSub=true should hide for guest",
		},
		{
			name: "sub path with HSub=false",
			user: &model.User{
				Role: model.GUEST,
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: false,
			},
			path: "/folder/subfolder",
			want: false,
			reason: "sub path with HSub=false should not hide",
		},
		{
			name: "non-sub path with HSub=true",
			user: &model.User{
				Role: model.GUEST,
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: true,
			},
			path: "/other",
			want: false,
			reason: "non-sub path should not hide even with HSub=true",
		},
		{
			name: "user without can_see_hides permission",
			user: &model.User{
				Role:       model.GENERAL,
				Permission: 0, // bit 0 not set = cannot see hides
			},
			meta: &model.Meta{
				Path: "/folder",
				Hide: "secret",
				HSub: true,
			},
			path: "/folder",
			want: true,
			reason: "user without can_see_hides permission should hide",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := whetherHide(tt.user, tt.meta, tt.path)
			if got != tt.want {
				t.Errorf("whetherHide() = %v, want %v\nReason: %s",
					got, tt.want, tt.reason)
			}
		})
	}
}
