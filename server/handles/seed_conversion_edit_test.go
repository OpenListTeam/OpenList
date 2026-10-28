package handles

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/tache"
	"github.com/gin-gonic/gin"
)

func TestDisplaySeedTaskDoesNotRegisterRetryRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	options := tache.DefaultOptions()
	options.Running = false
	manager := tache.NewManager[*fs.SeedSaveTask](tache.WithOptions(*options))
	engine := gin.New()
	taskRoute(engine.Group("/seed_save"), manager, false)
	routes := make(map[string]bool)
	for _, route := range engine.Routes() {
		routes[route.Path] = true
	}
	for _, suffix := range []string{"/retry", "/retry_some", "/retry_failed"} {
		if routes["/seed_save"+suffix] {
			t.Fatalf("display task exposes %s", suffix)
		}
	}
	for _, suffix := range []string{"/done", "/undone", "/cancel", "/delete"} {
		if !routes["/seed_save"+suffix] {
			t.Fatalf("missing display task route %s", suffix)
		}
	}
}

func TestSelectSeedConvertFiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		indexes []int
		changed bool
		invalid bool
	}{
		{"all omitted", nil, false, false},
		{"all selected", []int{1, 0}, false, false},
		{"subset", []int{1, 1}, true, false},
		{"empty", []int{}, false, true},
		{"out of bounds", []int{2}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := crossingSeed()
			seed.GlobalPieces = []string{strings.Repeat("a", 40)}
			changed, err := selectSeedConvertFiles(seed, tc.indexes)
			if changed != tc.changed || (err != nil) != tc.invalid {
				t.Fatalf("changed=%v err=%v", changed, err)
			}
			if changed && (len(seed.Files) != 1 || seed.Files[0].Path != "b.pdf" || len(seed.GlobalPieces) != 0) {
				t.Fatalf("invalid subset: %#v", seed)
			}
		})
	}
}

func TestSeedConvertMatrixPreservesRequiredAndOptionalHashes(t *testing.T) {
	requested := SeedHashMatrix{SHA256: SeedHashSelection{Whole: true, Pieces: true}}
	for _, target := range []string{"torrent", "cas", "oss"} {
		matrix := seedConvertMatrix(&requested, target)
		if matrix.SHA256 != requested.SHA256 {
			t.Fatalf("%s dropped optional SHA256", target)
		}
		if target == "torrent" && (!matrix.SHA1.Whole || !matrix.SHA1.Pieces) {
			t.Fatal("torrent requirements missing")
		}
		if target == "cas" && (!matrix.MD5.Whole || !matrix.MD5.Pieces) {
			t.Fatal("CAS requirements missing")
		}
	}
	seed := crossingSeed()
	if !seedConvertMatrixMissing(seed, requested) {
		t.Fatal("missing optional SHA256 was ignored")
	}
	if seedConvertMatrixMissing(seed, SeedHashMatrix{}) {
		t.Fatal("removing optional hashes should not read files")
	}
	if requested.SHA1.Whole || requested.MD5.Whole {
		t.Fatal("target requirements mutated the request")
	}
}

func TestSeedSaveResultsError(t *testing.T) {
	if err := seedSaveResultsError([]gin.H{{"method": "rapid_upload"}, {"method": "put_url"}}); err != nil {
		t.Fatal(err)
	}
	if err := seedSaveResultsError([]gin.H{{"method": "unavailable"}, {"method": "put_url", "error": "failed"}}); err == nil {
		t.Fatal("partial failure was treated as success")
	}
}

func TestConvertSeedIdentityCommentAndSelection(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name      string
		selected  []int
		wantFiles int
		target    string
	}{
		{"identity", nil, 2, "oss"},
		{"selected file", []int{1}, 1, "oss"},
		{"CAS comment", nil, 2, "cas"},
		{"CAS selected file", []int{1}, 1, "cas"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seed := crossingSeed()
			seed.Comment = "old"
			raw, err := torrent.EncodeOSS(seed)
			if err != nil {
				t.Fatal(err)
			}
			comment := "new"
			req := SeedConvertReq{SeedDataReq: SeedDataReq{SeedData: base64.StdEncoding.EncodeToString(raw), Format: "oss"}, Formats: []string{tc.target}, Comment: &comment, SelectedFiles: tc.selected}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			engine := gin.New()
			engine.POST("/convert", func(c *gin.Context) {
				c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), conf.UserKey, &model.User{ID: 1, BasePath: "/"}))
				ConvertSeed(c)
			})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(http.MethodPost, "/convert", strings.NewReader(string(body)))
			r.Header.Set("Content-Type", "application/json")
			engine.ServeHTTP(w, r)
			var resp struct {
				Code int `json:"code"`
				Data struct {
					SeedData string `json:"seed_data"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Code != 200 {
				t.Fatalf("response=%s", w.Body.String())
			}
			data, err := base64.StdEncoding.DecodeString(resp.Data.SeedData)
			if err != nil {
				t.Fatal(err)
			}
			result, err := torrent.DecodeSeed(data, tc.target, torrent.DefaultParseLimits())
			if err != nil {
				t.Fatal(err)
			}
			if result.Comment != comment || len(result.Files) != tc.wantFiles {
				t.Fatalf("comment=%q files=%d", result.Comment, len(result.Files))
			}
		})
	}
}
