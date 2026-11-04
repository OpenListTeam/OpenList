package driver

import (
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// Names are never trusted on their looks: only a tag this server computed for
// this very storage makes one.
func TestStagingCreatedRejectsOtherNames(t *testing.T) {
	st := &model.Storage{ID: 7}
	valid := StagingNameAt(st, time.Unix(1_800_000_000, 0))
	forged := valid[:len(valid)-1] + "0"
	if forged == valid {
		forged = valid[:len(valid)-1] + "1"
	}
	names := []string{
		".openlist-upload-0123456789abcdef", // what a user might name a file
		".openlist-upload-ABCDEFGHabcdefgh",
		".openlist-upload-notes.txt",
		StagingNameAt(&model.Storage{ID: 8}, time.Now()), // another storage's, e.g. one layered on this
		forged,
		valid + "0",
		strings.TrimPrefix(valid, "."),
		"report.pdf",
	}
	if upper := valid[:len(valid)-16] + strings.ToUpper(valid[len(valid)-16:]); upper != valid {
		names = append(names, upper)
	}
	for _, name := range names {
		if _, ok := StagingCreated(st, name); ok {
			t.Errorf("StagingCreated(%q) = true, want a name this storage never staged", name)
		}
	}
}
