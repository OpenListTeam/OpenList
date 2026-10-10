package lsky_pro

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestUniqueNames(t *testing.T) {
	objs := []model.Obj{
		Album{ID: 1, Name: "pics"}.toObj(),
		Album{ID: 2, Name: "pics"}.toObj(),
		Image{Key: "aaa", OriginName: "a.png"}.toObj(),
		Image{Key: "bbb", OriginName: "a.png"}.toObj(),
		Image{Key: "ccc", OriginName: "a.png"}.toObj(),
		Image{Key: "ddd", OriginName: "noext"}.toObj(),
		Image{Key: "eee", OriginName: "noext"}.toObj(),
	}
	uniqueNames(objs)
	want := []string{"pics", "pics (2)", "a.png", "a (bbb).png", "a (ccc).png", "noext", "noext (eee)"}
	for i, o := range objs {
		if o.GetName() != want[i] {
			t.Errorf("objs[%d] = %q, want %q", i, o.GetName(), want[i])
		}
	}
	// ids are untouched, so Link/Remove still hit the right record
	if objs[3].GetID() != "bbb" {
		t.Errorf("id changed: %s", objs[3].GetID())
	}
}
