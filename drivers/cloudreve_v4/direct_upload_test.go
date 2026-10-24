package cloudreve_v4

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/directupload"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

const (
	testSessionID = "session-1"
	testSecret    = "secret-1"
	testChunkSize = int64(4)
)

// fakeCloudreve answers the subset of the v4 API the direct upload path uses.
type fakeCloudreve struct {
	policyType  string
	policyRelay bool
	totalParts  int
	// conflictOnce makes the first upload session request fail with the
	// "Object existed" code Cloudreve returns for an existing file.
	conflictOnce bool

	mu             sync.Mutex
	uploadBodies   []map[string]any
	sessionDeleted bool
	completeBody   string
	callbackHit    bool
}

func (f *fakeCloudreve) handleFile(w http.ResponseWriter, r *http.Request) {
	writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
		"storage_policy": map[string]any{"id": "policy-1", "type": f.policyType},
	}})
}

func (f *fakeCloudreve) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.sessionDeleted = true
		f.mu.Unlock()
		writeTestJSON(w, map[string]any{"code": 0})
		return
	}

	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.uploadBodies = append(f.uploadBodies, body)
	conflict := f.conflictOnce && len(f.uploadBodies) == 1
	f.mu.Unlock()

	if conflict {
		writeTestJSON(w, map[string]any{"code": 40004, "msg": "Object existed"})
		return
	}

	base := "http://" + r.Host
	urls := make([]string, 0, f.totalParts)
	for i := 0; i < f.totalParts; i++ {
		urls = append(urls, fmt.Sprintf("%s/s3/part/%s/%d", base, testSessionID, i))
	}
	writeTestJSON(w, map[string]any{"code": 0, "data": map[string]any{
		"session_id":      testSessionID,
		"chunk_size":      testChunkSize,
		"upload_urls":     urls,
		"completeURL":     base + "/s3/complete",
		"callback_secret": testSecret,
		"uri":             body["uri"],
		"storage_policy": map[string]any{
			"id":    "policy-1",
			"type":  f.policyType,
			"relay": f.policyRelay,
		},
	}})
}

func (f *fakeCloudreve) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/file", f.handleFile)
	mux.HandleFunc("/api/v4/file/upload", f.handleUpload)
	mux.HandleFunc("/api/v4/callback/s3/"+testSessionID+"/"+testSecret,
		func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			f.callbackHit = true
			f.mu.Unlock()
			writeTestJSON(w, map[string]any{"code": 0})
		})
	mux.HandleFunc("/s3/complete", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.completeBody = string(data)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestDriver(t *testing.T, f *fakeCloudreve) *CloudreveV4 {
	t.Helper()
	srv := f.server(t)
	d := &CloudreveV4{}
	d.Address = srv.URL
	d.AccessToken = "test-token"
	d.Storage = model.Storage{ID: 1, MountPath: "/cr"}
	return d
}

func testContext(userID uint) context.Context {
	ctx := context.WithValue(context.Background(), conf.UserKey, &model.User{ID: userID})
	return context.WithValue(ctx, conf.DirectUploadContentTypeKey, "image/png")
}

// TestMain wires the HTTP clients the driver relies on: they are normally built
// during bootstrap, which a unit test never runs.
func TestMain(m *testing.M) {
	if base.RestyClient == nil {
		base.RestyClient = resty.New()
	}
	if base.HttpClient == nil {
		base.HttpClient = &http.Client{}
	}
	os.Exit(m.Run())
}

func TestDirectUploadInfoAndFinish(t *testing.T) {
	f := &fakeCloudreve{policyType: "s3", totalParts: 3}
	d := newTestDriver(t, f)

	info, err := d.GetDirectUploadInfo(testContext(7), DirectUploadToolHttp,
		&model.Object{Path: "/dir"}, "a.bin", 9)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	httpInfo, ok := info.(*model.HttpDirectUploadInfo)
	if !ok {
		t.Fatalf("GetDirectUploadInfo returned %T, want *model.HttpDirectUploadInfo", info)
	}
	if len(httpInfo.UploadURLs) != 3 {
		t.Fatalf("upload urls = %d, want 3", len(httpInfo.UploadURLs))
	}
	if httpInfo.ChunkSize != testChunkSize {
		t.Fatalf("chunk size = %d, want %d", httpInfo.ChunkSize, testChunkSize)
	}
	if httpInfo.Method != http.MethodPut {
		t.Fatalf("method = %q, want PUT", httpInfo.Method)
	}
	// s3 presigned urls are not signed with a content type, only ks3 needs one
	if len(httpInfo.Headers) != 0 {
		t.Fatalf("headers = %v, want none for s3", httpInfo.Headers)
	}

	token, ok := strings.CutPrefix(httpInfo.CompleteURL, "/fs/complete_direct_upload?token=")
	if !ok || token == "" {
		t.Fatalf("complete url = %q, want the completion endpoint with a token", httpInfo.CompleteURL)
	}
	session, err := directupload.Get(token, 7)
	if err != nil {
		t.Fatalf("directupload.Get: %v", err)
	}
	if session.Path != "/cr/dir/a.bin" {
		t.Fatalf("session path = %q, want %q", session.Path, "/cr/dir/a.bin")
	}

	if len(f.uploadBodies) != 1 {
		t.Fatalf("upload session requests = %d, want 1", len(f.uploadBodies))
	}
	body := f.uploadBodies[0]
	if body["uri"] != "/dir/a.bin" {
		t.Fatalf("session uri = %v, want /dir/a.bin", body["uri"])
	}
	if body["mime_type"] != "image/png" {
		t.Fatalf("session mime_type = %v, want image/png", body["mime_type"])
	}
	if _, exists := body["entity_type"]; exists {
		t.Fatalf("new upload sent entity_type = %v", body["entity_type"])
	}

	etags := []string{`"e1"`, `"e2"`, `"e3"`}
	if err = session.Finish(context.Background(), etags); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	want := `<Part><PartNumber>2</PartNumber><ETag>"e2"</ETag></Part>`
	if !strings.Contains(f.completeBody, want) {
		t.Fatalf("complete body = %q, want it to contain %q", f.completeBody, want)
	}
	if !f.callbackHit {
		t.Fatal("the Cloudreve callback was not called")
	}

	directupload.Delete(token)
}

func TestDirectUploadKs3SendsContentType(t *testing.T) {
	f := &fakeCloudreve{policyType: "ks3", totalParts: 1}
	d := newTestDriver(t, f)

	info, err := d.GetDirectUploadInfo(testContext(7), DirectUploadToolHttp,
		&model.Object{Path: "/"}, "b.bin", 2)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	httpInfo := info.(*model.HttpDirectUploadInfo)
	if httpInfo.Headers["Content-Type"] != "application/octet-stream" {
		t.Fatalf("headers = %v, want octet-stream content type", httpInfo.Headers)
	}
}

func TestDirectUploadUnsupportedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		typ   string
		relay bool
	}{
		{name: "local", typ: "local"},
		{name: "relay", typ: "s3", relay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeCloudreve{policyType: tc.typ, policyRelay: tc.relay, totalParts: 1}
			d := newTestDriver(t, f)

			_, err := d.GetDirectUploadInfo(testContext(7), DirectUploadToolHttp,
				&model.Object{Path: "/"}, "c.bin", 3)
			if !errs.IsNotImplementError(err) {
				t.Fatalf("GetDirectUploadInfo error = %v, want NotImplement", err)
			}
			// an unusable session must not be left open on Cloudreve
			if !f.sessionDeleted {
				t.Fatal("the abandoned upload session was not deleted")
			}
		})
	}
}

func TestDirectUploadRejectsWrongEtagCount(t *testing.T) {
	f := &fakeCloudreve{policyType: "s3", totalParts: 2}
	d := newTestDriver(t, f)

	info, err := d.GetDirectUploadInfo(testContext(7), DirectUploadToolHttp,
		&model.Object{Path: "/"}, "d.bin", 8)
	if err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	token, _ := strings.CutPrefix(info.(*model.HttpDirectUploadInfo).CompleteURL,
		"/fs/complete_direct_upload?token=")
	session, err := directupload.Get(token, 7)
	if err != nil {
		t.Fatalf("directupload.Get: %v", err)
	}
	if err = session.Finish(context.Background(), []string{`"only-one"`}); err == nil {
		t.Fatal("Finish accepted a wrong number of etags")
	}
	directupload.Delete(token)
}

func TestDirectUploadFallsBackToNewVersion(t *testing.T) {
	f := &fakeCloudreve{policyType: "s3", totalParts: 1, conflictOnce: true}
	d := newTestDriver(t, f)

	if _, err := d.GetDirectUploadInfo(testContext(7), DirectUploadToolHttp,
		&model.Object{Path: "/"}, "e.bin", 2); err != nil {
		t.Fatalf("GetDirectUploadInfo: %v", err)
	}
	if len(f.uploadBodies) != 2 {
		t.Fatalf("upload session requests = %d, want 2 (retry as version)", len(f.uploadBodies))
	}
	if f.uploadBodies[1]["entity_type"] != "version" {
		t.Fatalf("retry entity_type = %v, want version", f.uploadBodies[1]["entity_type"])
	}
}

func TestDirectUploadDisabledByOption(t *testing.T) {
	f := &fakeCloudreve{policyType: "s3", totalParts: 1}
	d := newTestDriver(t, f)
	d.DisableDirectUpload = true

	if tools := d.GetDirectUploadTools(); len(tools) != 0 {
		t.Fatalf("GetDirectUploadTools = %v, want none when disabled", tools)
	}
	if _, err := d.GetDirectUploadInfo(testContext(7), DirectUploadToolHttp,
		&model.Object{Path: "/"}, "f.bin", 2); !errs.IsNotImplementError(err) {
		t.Fatalf("GetDirectUploadInfo error = %v, want NotImplement", err)
	}
	if len(f.uploadBodies) != 0 {
		t.Fatal("a session was created while direct upload is disabled")
	}
}

func writeTestJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
