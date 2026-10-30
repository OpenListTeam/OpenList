package authz

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestMetaCoversPath(t *testing.T) {
	tests := []struct {
		name, metaPath, reqPath string
		sub, want               bool
	}{
		{"exact", "/folder", "/folder", false, true},
		{"exact case insensitive", "/Folder", "/folder", false, true},
		{"child enabled", "/folder", "/folder/child", true, true},
		{"deep child case insensitive", "/Folder", "/folder/child/file", true, true},
		{"child disabled", "/folder", "/folder/child", false, false},
		{"sibling", "/folder", "/other", true, false},
		{"prefix sibling", "/folder", "/folder-name", true, false},
		{"root subtree", "/", "/any/deep/path", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MetaCoversPath(tt.metaPath, tt.reqPath, tt.sub); got != tt.want {
				t.Fatalf("MetaCoversPath(%q, %q, %v) = %v, want %v", tt.metaPath, tt.reqPath, tt.sub, got, tt.want)
			}
		})
	}
}

func TestReadAndWriteRestrictions(t *testing.T) {
	tests := []struct {
		name string
		user *model.User
		meta *model.Meta
		path string
		want bool
	}{
		{"system context", nil, restrictedMeta(false), "/folder", true},
		{"no meta", &model.User{ID: 5}, nil, "/folder", true},
		{"unrestricted", &model.User{ID: 5}, &model.Meta{Path: "/folder"}, "/folder", true},
		{"listed exact", &model.User{ID: 1}, restrictedMeta(false), "/folder", true},
		{"unlisted exact", &model.User{ID: 5}, restrictedMeta(false), "/folder", false},
		{"unlisted child not inherited", &model.User{ID: 5}, restrictedMeta(false), "/folder/child", true},
		{"unlisted child inherited", &model.User{ID: 5}, restrictedMeta(true), "/folder/child", false},
		{"listed deep child", &model.User{ID: 1}, restrictedMeta(true), "/folder/child/file", true},
		{"restriction path is case insensitive", &model.User{ID: 5}, restrictedMeta(true), "/Folder/child", false},
		{"unrelated path", &model.User{ID: 5}, restrictedMeta(true), "/other", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanRead(tt.user, tt.meta, tt.path); got != tt.want {
				t.Errorf("CanRead() = %v, want %v", got, tt.want)
			}
			if got := CanWrite(tt.user, tt.meta, tt.path); got != tt.want {
				t.Errorf("CanWrite() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCanWriteContentBypassUserPerms(t *testing.T) {
	tests := []struct {
		name string
		meta *model.Meta
		path string
		want bool
	}{
		{"no meta", nil, "/folder", false},
		{"disabled", &model.Meta{Path: "/folder"}, "/folder", false},
		{"exact", &model.Meta{Path: "/folder", Write: true}, "/folder", true},
		{"child enabled", &model.Meta{Path: "/folder", Write: true, WSub: true}, "/folder/child", true},
		{"child disabled", &model.Meta{Path: "/folder", Write: true}, "/folder/child", false},
		{"unrelated", &model.Meta{Path: "/folder", Write: true, WSub: true}, "/other", false},
		{"case mismatch does not grant", &model.Meta{Path: "/Folder", Write: true, WSub: true}, "/folder/child", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanWriteContentBypassUserPerms(tt.meta, tt.path); got != tt.want {
				t.Fatalf("CanWriteContentBypassUserPerms() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsHidden(t *testing.T) {
	hideMeta := func(sub bool) *model.Meta {
		return &model.Meta{Path: "/folder", Hide: "^secret$\n\\.private$", HSub: sub}
	}
	tests := []struct {
		name string
		user *model.User
		meta *model.Meta
		path string
		want bool
	}{
		{"system context", nil, hideMeta(true), "/folder/secret", false},
		{"hide-capable user", &model.User{Permission: 1}, hideMeta(true), "/folder/secret", false},
		{"no meta", &model.User{}, nil, "/folder/secret", false},
		{"empty patterns", &model.User{}, &model.Meta{Path: "/folder", HSub: true}, "/folder/secret", false},
		{"matching child", &model.User{}, hideMeta(false), "/folder/secret", true},
		{"nonmatching child", &model.User{}, hideMeta(false), "/folder/public", false},
		{"second pattern", &model.User{}, hideMeta(false), "/folder/file.private", true},
		{"nested enabled", &model.User{}, hideMeta(true), "/folder/child/secret", true},
		{"nested disabled", &model.User{}, hideMeta(false), "/folder/child/secret", false},
		{"unrelated parent", &model.User{}, hideMeta(true), "/other/secret", false},
		{"coverage is case insensitive", &model.User{}, hideMeta(true), "/Folder/secret", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsHidden(tt.user, tt.meta, tt.path); got != tt.want {
				t.Fatalf("IsHidden() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFilterHidden(t *testing.T) {
	objs := []model.Obj{
		&model.Object{Name: "public"},
		&model.Object{Name: "secret"},
		&model.Object{Name: "file.private"},
	}
	meta := &model.Meta{Path: "/folder", Hide: "^secret$\n\\.private$"}

	got := FilterHidden(&model.User{}, meta, "/folder", objs)
	if len(got) != 1 || got[0].GetName() != "public" {
		t.Fatalf("FilterHidden() returned %v, want only public", objectNames(got))
	}
}

func TestCanAccess(t *testing.T) {
	user := func(id uint, permission int32) *model.User {
		return &model.User{ID: id, Permission: permission}
	}
	tests := []struct {
		name     string
		user     *model.User
		meta     *model.Meta
		path     string
		password string
		want     bool
	}{
		{"no policy", user(1, 0), nil, "/file", "", true},
		{"hidden name", user(1, 0), &model.Meta{Path: "/", Hide: "^secret$", HSub: true}, "/secret", "", false},
		{"hidden permission bypass", user(1, 1), &model.Meta{Path: "/", Hide: "^secret$", HSub: true}, "/secret", "", true},
		{"read restriction precedes password", user(5, 0), accessMeta(), "/folder/file", "secret", false},
		{"correct password", user(1, 0), accessMeta(), "/folder/file", "secret", true},
		{"wrong password", user(1, 0), accessMeta(), "/folder/file", "wrong", false},
		{"password permission bypass", user(1, 2), accessMeta(), "/folder/file", "wrong", true},
		{"password not inherited", user(1, 0), &model.Meta{Path: "/folder", Password: "secret"}, "/folder/file", "wrong", true},
		{"case-insensitive exact password coverage", user(1, 0), &model.Meta{Path: "/Folder", Password: "secret"}, "/folder", "wrong", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CanAccess(tt.user, tt.meta, tt.path, tt.password); got != tt.want {
				t.Fatalf("CanAccess() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWritePermissionCombination(t *testing.T) {
	tests := []struct {
		name string
		user *model.User
		meta *model.Meta
		path string
		want bool
	}{
		{"user permission and listed", &model.User{ID: 1, Permission: 1 << 3}, writeMeta(false, []uint{1}, false), "/folder", true},
		{"user permission but unlisted", &model.User{ID: 5, Permission: 1 << 3}, writeMeta(false, []uint{1}, false), "/folder", false},
		{"meta bypass and listed", &model.User{ID: 1}, writeMeta(true, []uint{1}, false), "/folder", true},
		{"meta bypass but unlisted", &model.User{ID: 5}, writeMeta(true, []uint{1}, false), "/folder", false},
		{"no permission or bypass", &model.User{ID: 1}, writeMeta(false, []uint{1}, false), "/folder", false},
		{"inherited bypass and whitelist", &model.User{ID: 1}, writeMeta(true, []uint{1}, true), "/folder/child", true},
		{"non-inherited bypass", &model.User{ID: 1}, writeMeta(true, []uint{1}, false), "/folder/child", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (tt.user.CanWriteContent() || CanWriteContentBypassUserPerms(tt.meta, tt.path)) &&
				CanWrite(tt.user, tt.meta, tt.path)
			if got != tt.want {
				t.Fatalf("combined write permission = %v, want %v", got, tt.want)
			}
		})
	}
}

func restrictedMeta(sub bool) *model.Meta {
	return &model.Meta{
		Path:          "/folder",
		ReadUsers:     []uint{1, 2},
		ReadUsersSub:  sub,
		WriteUsers:    []uint{1, 2},
		WriteUsersSub: sub,
	}
}

func accessMeta() *model.Meta {
	return &model.Meta{
		Path:         "/folder",
		ReadUsers:    []uint{1, 2},
		ReadUsersSub: true,
		Password:     "secret",
		PSub:         true,
	}
}

func writeMeta(write bool, users []uint, sub bool) *model.Meta {
	return &model.Meta{
		Path:          "/folder",
		Write:         write,
		WSub:          sub,
		WriteUsers:    users,
		WriteUsersSub: sub,
	}
}

func objectNames(objs []model.Obj) []string {
	names := make([]string, len(objs))
	for i, obj := range objs {
		names[i] = obj.GetName()
	}
	return names
}
