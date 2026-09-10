package common

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestGetEtagSubsecondChanges(t *testing.T) {
	file := &model.Object{Size: 4, Modified: time.Unix(1700000000, 100)}
	before := GetEtag(file, file.Size)
	if got := GetEtag(file, file.Size); got != before {
		t.Fatalf("unchanged file ETag = %q, want %q", got, before)
	}
	file.Modified = file.Modified.Add(time.Nanosecond)
	if got := GetEtag(file, file.Size); got == before {
		t.Errorf("same-size overwrite within one second retained ETag %q", got)
	}
	file.Modified = file.Modified.Add(-time.Nanosecond)
	if got := GetEtag(file, file.Size+1); got == before {
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
