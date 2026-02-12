package common

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestCoversPath(t *testing.T) {
	tests := []struct {
		name     string
		metaPath string
		reqPath  string
		applySub bool
		want     bool
	}{
		{
			name:     "exact path match with applySub=false",
			metaPath: "/folder",
			reqPath:  "/folder",
			applySub: false,
			want:     true,
		},
		{
			name:     "exact path match with applySub=true",
			metaPath: "/folder",
			reqPath:  "/folder",
			applySub: true,
			want:     true,
		},
		{
			name:     "sub path with applySub=true",
			metaPath: "/folder",
			reqPath:  "/folder/subfolder",
			applySub: true,
			want:     true,
		},
		{
			name:     "sub path with applySub=false",
			metaPath: "/folder",
			reqPath:  "/folder/subfolder",
			applySub: false,
			want:     false,
		},
		{
			name:     "non-sub path with applySub=true",
			metaPath: "/folder",
			reqPath:  "/other",
			applySub: true,
			want:     false,
		},
		{
			name:     "non-sub path with applySub=false",
			metaPath: "/folder",
			reqPath:  "/other",
			applySub: false,
			want:     false,
		},
		{
			name:     "root path covers all with applySub=true",
			metaPath: "/",
			reqPath:  "/any/deep/path",
			applySub: true,
			want:     true,
		},
		{
			name:     "root path exact match",
			metaPath: "/",
			reqPath:  "/",
			applySub: false,
			want:     true,
		},
		{
			name:     "deep sub path with applySub=true",
			metaPath: "/folder",
			reqPath:  "/folder/sub1/sub2/file.txt",
			applySub: true,
			want:     true,
		},
		{
			name:     "sibling paths with applySub=true",
			metaPath: "/folder1",
			reqPath:  "/folder2",
			applySub: true,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MetaCoversPath(tt.metaPath, tt.reqPath, tt.applySub)
			if got != tt.want {
				t.Errorf("MetaCoversPath(%q, %q, %v) = %v, want %v",
					tt.metaPath, tt.reqPath, tt.applySub, got, tt.want)
			}
		})
	}
}

func TestCanWriteContentIgnoringUserPerms(t *testing.T) {
	tests := []struct {
		name   string
		meta   *model.Meta
		path   string
		want   bool
		reason string
	}{
		{
			name:   "nil meta",
			meta:   nil,
			path:   "/any",
			want:   false,
			reason: "nil meta should deny write",
		},
		{
			name: "meta.Write=false",
			meta: &model.Meta{
				Path:  "/folder",
				Write: false,
			},
			path:   "/folder",
			want:   false,
			reason: "Write=false should deny write",
		},
		{
			name: "exact path match with WSub=false",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  false,
			},
			path:   "/folder",
			want:   true,
			reason: "exact path match should allow write",
		},
		{
			name: "sub path with WSub=true",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  true,
			},
			path:   "/folder/subfolder",
			want:   true,
			reason: "sub path with WSub=true should allow write",
		},
		{
			name: "sub path with WSub=false (BEHAVIOR CHANGE)",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  false,
			},
			path:   "/folder/subfolder",
			want:   false,
			reason: "sub path with WSub=false should deny write (fixed bug)",
		},
		{
			name: "non-sub path with WSub=true",
			meta: &model.Meta{
				Path:  "/folder",
				Write: true,
				WSub:  true,
			},
			path:   "/other",
			want:   false,
			reason: "non-sub path should deny write even with WSub=true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CanWriteContentBypassUserPerms(tt.meta, tt.path)
			if got != tt.want {
				t.Errorf("CanWriteContentBypassUserPerms() = %v, want %v\nReason: %s",
					got, tt.want, tt.reason)
			}
		})
	}
}
