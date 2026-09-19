package common

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestGetEtagStableAcrossCacheRefresh(t *testing.T) {
	uploaded := &model.Object{Size: 4, Modified: time.Unix(1700000000, 123456789)}
	before := GetEtag(uploaded, uploaded.Size)
	refreshed := &model.Object{Size: uploaded.Size, Modified: time.Unix(1700000000, 0)}
	if got := GetEtag(refreshed, refreshed.Size); got != before {
		t.Fatalf("refreshed file ETag = %q, want %q", got, before)
	}
	refreshed.Modified = refreshed.Modified.Add(time.Second)
	if got := GetEtag(refreshed, refreshed.Size); got == before {
		t.Errorf("changed modification second retained ETag %q", got)
	}
	refreshed.Modified = uploaded.Modified
	if got := GetEtag(refreshed, refreshed.Size+1); got == before {
		t.Errorf("changed size retained ETag %q", got)
	}
}

func TestGetEtagContentHash(t *testing.T) {
	file := &model.Object{
		Size:     4,
		HashInfo: utils.NewHashInfo(utils.SHA256, utils.HashData(utils.SHA256, []byte("data"))),
	}
	before := GetEtag(file, file.Size)
	file.Modified = time.Unix(1700000000, 100)
	if got := GetEtag(file, file.Size); got != before {
		t.Errorf("unchanged content ETag = %q, want %q", got, before)
	}
	file.HashInfo = utils.NewHashInfo(utils.SHA256, utils.HashData(utils.SHA256, []byte("next")))
	if got := GetEtag(file, file.Size); got == before {
		t.Errorf("changed content retained ETag %q", got)
	}
}
