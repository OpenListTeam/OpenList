package handles

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// The heatmap cuts are compared against last_access, which every writer stores
// in UTC. A cutoff built from time.Now() carries the server's own offset, and
// the comparison is then off by that offset: with TZ=+08 a file read 20 hours
// ago fell outside the "last 24 hours" bin. Every other cutoff in this package
// and in internal/access is built from time.Now().UTC() for exactly this reason.
func TestHeatmapCutoffsAreUTC(t *testing.T) {
	saved := time.Local
	time.Local = time.FixedZone("CST", 8*3600)
	defer func() { time.Local = saved }()

	d, err := gorm.Open(sqlite.Open("file:heatmap?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(d)

	read := time.Now().UTC().Add(-20 * time.Hour)
	if err := d.Create(&model.FileAccess{Path: "/热层/a.mkv", LastAccess: &read, Bucket: "hot"}).Error; err != nil {
		t.Fatal(err)
	}

	heatmap, err := accessHeatmap()
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := heatmap["rows"].([]gin.H)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected one heatmap row, got %#v", heatmap["rows"])
	}
	cells := rows[0]["cells"].([]int64)
	if cells[0] != 1 {
		t.Fatalf("a file read 20 hours ago belongs in the last-24-hours bin, got cells %v", cells)
	}
}
