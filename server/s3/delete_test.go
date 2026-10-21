package s3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/gofakes3"
)

const removeErrDriverName = "S3RemoveErrLocal"

// removeErr is the error the fixture driver's Remove returns (nil = delete normally).
var removeErr error

type removeErrDriver struct{ local.Local }

func (d *removeErrDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = removeErrDriverName
	return c
}

func (d *removeErrDriver) Remove(ctx context.Context, obj model.Obj) error {
	if removeErr != nil {
		return removeErr
	}
	return d.Local.Remove(ctx, obj)
}

func setupDeleteServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	ctx := context.Background()
	op.RegisterDriver(func() driver.Driver { return &removeErrDriver{} })
	root := t.TempDir()
	addition, _ := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: root})
	mount := "/" + sanitizeTestName(t.Name())
	id, err := op.CreateStorage(ctx, model.Storage{Driver: removeErrDriverName, MountPath: mount, Addition: string(addition)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = op.DeleteStorageById(ctx, id) })
	if err := op.SaveSettingItem(&model.SettingItem{Key: conf.S3Buckets, Value: `[{"name":"del","path":"` + mount + `"}]`}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gofakes3.New(newBackend(), gofakes3.WithoutVersioning()).Server())
	t.Cleanup(srv.Close)
	t.Cleanup(func() { removeErr = nil })
	return srv, root
}

func doReq(t *testing.T, method, url, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(method, url, strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestDeleteObjectReportsRemoveErrors(t *testing.T) {
	srv, root := setupDeleteServer(t)
	write := func(name string) string {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   gofakes3.ErrorCode
		wantGone   bool
	}{
		{"rate limited", errs.NewErr(errs.TemporaryCapacity, "baidu 31034"), http.StatusServiceUnavailable, gofakes3.ErrSlowDown, false},
		{"other error", errors.New("baidu errno 12"), http.StatusInternalServerError, gofakes3.ErrInternal, false},
		{"success", nil, http.StatusNoContent, "", true},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			name := "obj" + string(rune('a'+i))
			p := write(name)
			removeErr = c.err
			resp, body := doReq(t, http.MethodDelete, srv.URL+"/del/"+name, "")
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, c.wantStatus, body)
			}
			if c.wantCode != "" && !strings.Contains(body, "<Code>"+string(c.wantCode)+"</Code>") {
				t.Fatalf("body %q lacks code %s", body, c.wantCode)
			}
			if exists(p) == c.wantGone {
				t.Fatalf("file exists=%v, want gone=%v", exists(p), c.wantGone)
			}
		})
	}

	t.Run("missing key is success", func(t *testing.T) {
		removeErr = errors.New("must not be called")
		resp, body := doReq(t, http.MethodDelete, srv.URL+"/del/never-existed", "")
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("status = %d (%s)", resp.StatusCode, body)
		}
	})
}

func TestDeleteMultiReportsPerKeyErrors(t *testing.T) {
	srv, root := setupDeleteServer(t)
	for _, n := range []string{"k1", "k2"} {
		if err := os.WriteFile(filepath.Join(root, n), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removeErr = errs.NewErr(errs.TemporaryCapacity, "baidu 31034")
	resp, body := doReq(t, http.MethodPost, srv.URL+"/del?delete",
		`<Delete><Object><Key>k1</Key></Object><Object><Key>gone</Key></Object></Delete>`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d (%s)", resp.StatusCode, body)
	}
	var out struct {
		Deleted []struct{ Key string } `xml:"Deleted"`
		Error   []struct {
			Key  string
			Code string
		} `xml:"Error"`
	}
	if err := xml.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Error) != 1 || out.Error[0].Key != "k1" || out.Error[0].Code != string(gofakes3.ErrSlowDown) {
		t.Fatalf("errors = %+v, want k1 SlowDown (%s)", out.Error, body)
	}
	if len(out.Deleted) != 1 || out.Deleted[0].Key != "gone" {
		t.Fatalf("deleted = %+v, want [gone]", out.Deleted)
	}

	removeErr = errors.New("boom")
	_, body = doReq(t, http.MethodPost, srv.URL+"/del?delete", `<Delete><Object><Key>k2</Key></Object></Delete>`)
	if !strings.Contains(body, "<Code>InternalError</Code>") {
		t.Fatalf("want InternalError for k2: %s", body)
	}
}
