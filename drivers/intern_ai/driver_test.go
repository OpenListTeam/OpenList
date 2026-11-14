package intern_ai

import (
        "bytes"
        "context"
        "encoding/json"
        "net/http"
        "net/http/httptest"
        "os"
        "path"
        "strconv"
        "strings"
        "testing"
        "time"

        "github.com/OpenListTeam/OpenList/v4/drivers/base"
        "github.com/OpenListTeam/OpenList/v4/internal/conf"
        "github.com/OpenListTeam/OpenList/v4/internal/driver"
        "github.com/OpenListTeam/OpenList/v4/internal/model"
        "github.com/OpenListTeam/OpenList/v4/internal/stream"
)

func TestMain(m *testing.M) {
        dir, _ := os.MkdirTemp("", "internai-test")
        conf.Conf = conf.DefaultConfig(dir)
        base.InitClient()
        os.Exit(m.Run())
}

// newMockServer builds an in-memory cloud-disk API mock.
func newMockServer(t *testing.T) (*httptest.Server, map[string]fileItem) {
        t.Helper()
        store := map[string]fileItem{
                "/readme.txt":   {FileKey: "/readme.txt", FileName: "readme.txt", Size: 11, ContentType: "text/plain", ModifiedAt: "2026-10-24T08:00:00+08:00"},
                "/docs":         {FileKey: "/docs", FileName: "docs", IsDir: true, ModifiedAt: "2026-10-24T08:00:00+08:00"},
                "/docs/a.txt":   {FileKey: "/docs/a.txt", FileName: "a.txt", Size: 1, ContentType: "text/plain", ModifiedAt: "2026-10-24T09:00:00+08:00"},
                "/docs/b.bin":   {FileKey: "/docs/b.bin", FileName: "b.bin", Size: 2, ContentType: "application/octet-stream", ModifiedAt: "2026-10-24T09:00:00+08:00"},
                "/dir2":         {FileKey: "/dir2", FileName: "dir2", IsDir: true},
                "/dir2/c.txt":   {FileKey: "/dir2/c.txt", FileName: "c.txt", Size: 3},
                "/dir2/sub":     {FileKey: "/dir2/sub", FileName: "sub", IsDir: true},
                "/dir2/sub/d":   {FileKey: "/dir2/sub/d", FileName: "d", IsDir: true},
                "/dir2/sub/d/e": {FileKey: "/dir2/sub/d/e", FileName: "e", IsDir: true},
        }

        mux := http.NewServeMux()

        var baseURL string // set once the test server is listening

        writeOK := func(w http.ResponseWriter, data any) {
                w.Header().Set("Content-Type", "application/json")
                _ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "msg": "ok", "data": data})
        }

        // list: two pages when dir has >2 entries
        mux.HandleFunc("/api/ml/v1/cloud-disk/list", func(w http.ResponseWriter, r *http.Request) {
                if r.Header.Get("Authorization") != "Bearer test-token" {
                        w.WriteHeader(http.StatusUnauthorized)
                        _ = json.NewEncoder(w).Encode(map[string]any{"code": 401})
                        return
                }
                q := r.URL.Query()
                dirPath := q.Get("dir_path")
                pageToken := q.Get("page_token")
                pageSize := 2
                if ps := q.Get("page_size"); ps != "" {
                        if n, err := strconv.Atoi(ps); err == nil {
                                pageSize = n
                        }
                }
                var keys []string
                for k, f := range store {
                        if f.FileKey == k { // skip zero-value entries
                                if strings.HasPrefix(k, strings.TrimSuffix(dirPath, "/")+"/") || (dirPath == "/" && strings.HasPrefix(k, "/")) {
                                        if dirPath != "/" {
                                                rel := strings.TrimPrefix(k, strings.TrimSuffix(dirPath, "/")+"/")
                                                if strings.Contains(rel, "/") {
                                                        continue
                                                }
                                        } else if strings.Count(strings.Trim(k, "/"), "/") > 0 {
                                                continue
                                        }
                                        keys = append(keys, k)
                                }
                        }
                }
                // stable order
                for i := 0; i < len(keys); i++ {
                        for j := i + 1; j < len(keys); j++ {
                                if keys[j] < keys[i] {
                                        keys[i], keys[j] = keys[j], keys[i]
                                }
                        }
                }
                start := 0
                if pageToken != "" {
                        start, _ = strconv.Atoi(pageToken)
                }
                end := start + pageSize
                if end > len(keys) {
                        end = len(keys)
                }
                files := make([]fileItem, 0, end-start)
                for _, k := range keys[start:end] {
                        files = append(files, store[k])
                }
                next := ""
                if end < len(keys) {
                        next = strconv.Itoa(end)
                }
                writeOK(w, map[string]any{"files": files, "next_page_token": next, "total": len(keys), "has_more": next != ""})
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/quota", func(w http.ResponseWriter, r *http.Request) {
                if r.Header.Get("Authorization") != "Bearer test-token" {
                        w.WriteHeader(http.StatusUnauthorized)
                        _ = json.NewEncoder(w).Encode(map[string]any{"code": 401})
                        return
                }
                writeOK(w, map[string]any{"used_bytes": 10, "total_bytes": 100})
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/download-url", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        FileKeys []string `json:"file_keys"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                urls := make([]urlItem, 0, len(req.FileKeys))
                for _, k := range req.FileKeys {
                        f, ok := store[cleanKey(k)]
                        if !ok || f.IsDir {
                                urls = append(urls, urlItem{FileKey: k, Skipped: true})
                                continue
                        }
                        urls = append(urls, urlItem{FileKey: k, DownloadURL: "https://oss.example.com" + cleanKey(k) + "?sig=abc"})
                }
                writeOK(w, map[string]any{"urls": urls})
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/upload-url", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        DirPath string `json:"dir_path"`
                        Files   []struct {
                                FileName string `json:"file_name"`
                                FileSize int64  `json:"file_size"`
                                FileType string `json:"file_type"`
                        } `json:"files"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                urls := make([]urlItem, 0, len(req.Files))
                for _, f := range req.Files {
                        urls = append(urls, urlItem{FileName: f.FileName, UploadURL: baseURL + "/put"})
                }
                // Production quirk: the upload-url endpoint answers WITHOUT the
                // {code,msg,data} envelope (payload at the top level).
                w.Header().Set("Content-Type", "application/json")
                _ = json.NewEncoder(w).Encode(map[string]any{"urls": urls})
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/mkdir", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        DirPath string `json:"dir_path"`
                        DirName string `json:"dir_name"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                key := joinKey(req.DirPath, req.DirName)
                store[key] = fileItem{FileKey: key, FileName: req.DirName, IsDir: true}
                writeOK(w, nil)
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/rename", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        SrcKey  string `json:"src_key"`
                        NewName string `json:"new_name"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                f, ok := store[cleanKey(req.SrcKey)]
                if !ok {
                        writeOK(w, nil)
                        return
                }
                newKey := joinKey(path.Dir(cleanKey(req.SrcKey)), req.NewName)
                delete(store, cleanKey(req.SrcKey))
                f.FileKey, f.FileName = newKey, req.NewName
                store[newKey] = f
                writeOK(w, nil)
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/move", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        SrcKeys []string `json:"src_keys"`
                        DstDir  string   `json:"dst_dir"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                for _, k := range req.SrcKeys {
                        f, ok := store[cleanKey(k)]
                        if !ok {
                                continue
                        }
                        newKey := joinKey(req.DstDir, f.FileName)
                        delete(store, cleanKey(k))
                        f.FileKey = newKey
                        store[newKey] = f
                }
                writeOK(w, nil)
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/copy", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        SrcKeys []string `json:"src_keys"`
                        DstDir  string   `json:"dst_dir"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                for _, k := range req.SrcKeys {
                        f, ok := store[cleanKey(k)]
                        if !ok {
                                continue
                        }
                        newKey := joinKey(req.DstDir, f.FileName)
                        f.FileKey = newKey
                        store[newKey] = f
                }
                writeOK(w, nil)
        })

        mux.HandleFunc("/api/ml/v1/cloud-disk/delete", func(w http.ResponseWriter, r *http.Request) {
                var req struct {
                        FileKeys []string `json:"file_keys"`
                }
                _ = json.NewDecoder(r.Body).Decode(&req)
                for _, k := range req.FileKeys {
                        delete(store, cleanKey(k))
                }
                writeOK(w, nil)
        })

        // PUT target for uploads
        mux.HandleFunc("/put", func(w http.ResponseWriter, r *http.Request) {
                buf := make([]byte, 4096)
                for {
                        _, err := r.Body.Read(buf)
                        if err != nil {
                                break
                        }
                }
                w.WriteHeader(http.StatusOK)
        })

        srv := httptest.NewServer(mux)
        baseURL = srv.URL
        t.Cleanup(srv.Close)
        return srv, store
}

func newTestDriver(t *testing.T) (*InternAI, map[string]fileItem) {
        t.Helper()
        srv, store := newMockServer(t)
        d := &InternAI{}
        d.MountPath = "/internai"
        d.Addition.Token = "test-token"
        d.Addition.APIBase = srv.URL
        d.Addition.PageSize = 2
        d.Addition.RootFolderPath = "/"
        if err := d.Init(context.Background()); err != nil {
                t.Fatalf("init failed: %v", err)
        }
        return d, store
}

func TestInitInvalidToken(t *testing.T) {
        srv, _ := newMockServer(t)
        d := &InternAI{}
        d.MountPath = "/x"
        d.Addition.Token = "bad"
        d.Addition.APIBase = srv.URL
        if err := d.Init(context.Background()); err == nil {
                t.Fatal("expected init to fail with invalid token")
        }
}

func TestListPagination(t *testing.T) {
        d, _ := newTestDriver(t)
        root := &model.Object{Name: "/", Path: "/", IsFolder: true}
        objs, err := d.List(context.Background(), root, model.ListArgs{})
        if err != nil {
                t.Fatalf("list failed: %v", err)
        }
        names := map[string]bool{}
        for _, o := range objs {
                names[o.GetName()] = true
        }
        for _, want := range []string{"dir2", "docs", "readme.txt"} {
                if !names[want] {
                        t.Fatalf("expected %q in root listing, got %v", want, names)
                }
        }
}

func TestLink(t *testing.T) {
        d, _ := newTestDriver(t)
        link, err := d.Link(context.Background(), &model.Object{Name: "readme.txt", Path: "/readme.txt"}, model.LinkArgs{})
        if err != nil {
                t.Fatalf("link failed: %v", err)
        }
        if link.URL != "https://oss.example.com/readme.txt?sig=abc" {
                t.Fatalf("unexpected link url: %s", link.URL)
        }
        if _, err := d.Link(context.Background(), &model.Object{Name: "docs", Path: "/docs", IsFolder: true}, model.LinkArgs{}); err == nil {
                t.Fatal("expected error linking a folder")
        }
}

func TestWriteOps(t *testing.T) {
        d, store := newTestDriver(t)
        ctx := context.Background()
        root := &model.Object{Name: "/", Path: "/", IsFolder: true}

        // mkdir
        if _, err := d.MakeDir(ctx, root, "newdir"); err != nil {
                t.Fatalf("mkdir: %v", err)
        }
        if _, ok := store["/newdir"]; !ok {
                t.Fatal("newdir not created in mock store")
        }

        // rename
        if _, err := d.Rename(ctx, &model.Object{Name: "readme.txt", Path: "/readme.txt"}, "README.md"); err != nil {
                t.Fatalf("rename: %v", err)
        }
        if _, ok := store["/README.md"]; !ok {
                t.Fatal("renamed file missing")
        }

        // move
        if _, err := d.Move(ctx, &model.Object{Name: "README.md", Path: "/README.md"}, &model.Object{Name: "newdir", Path: "/newdir", IsFolder: true}); err != nil {
                t.Fatalf("move: %v", err)
        }
        if _, ok := store["/newdir/README.md"]; !ok {
                t.Fatal("moved file missing")
        }

        // copy
        if _, err := d.Copy(ctx, &model.Object{Name: "a.txt", Path: "/docs/a.txt"}, &model.Object{Name: "newdir", Path: "/newdir", IsFolder: true}); err != nil {
                t.Fatalf("copy: %v", err)
        }
        if _, ok := store["/newdir/a.txt"]; !ok {
                t.Fatal("copied file missing")
        }

        // remove
        if err := d.Remove(ctx, &model.Object{Name: "newdir/a.txt", Path: "/newdir/a.txt"}); err != nil {
                t.Fatalf("remove: %v", err)
        }
        if _, ok := store["/newdir/a.txt"]; ok {
                t.Fatal("file not removed")
        }
}

func TestPutUpload(t *testing.T) {
        d, _ := newTestDriver(t)
        ctx := context.Background()
        dst := &model.Object{Name: "docs", Path: "/docs", IsFolder: true}
        content := []byte("hello upload")
        fs := &stream.FileStream{
                Ctx:    ctx,
                Obj:    &model.Object{Name: "up.txt", Size: int64(len(content)), IsFolder: false, Modified: time.Now()},
                Reader: bytes.NewReader(content),
        }
        got, err := d.Put(ctx, dst, fs, driver.UpdateProgress(func(p float64) {}))
        if err != nil {
                t.Fatalf("put failed: %v", err)
        }
        if got.GetName() != "up.txt" || got.GetPath() != "/docs/up.txt" {
                t.Fatalf("unexpected put result: %+v", got)
        }
}

// TestNumericModifiedAt guards against the production response shape where
// modified_at is a unix-seconds JSON number instead of a string.
func TestNumericModifiedAt(t *testing.T) {
        mux := http.NewServeMux()
        mux.HandleFunc("/api/ml/v1/cloud-disk/quota", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                _, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"total_quota":30720,"used_quota":0}}`))
        })
        mux.HandleFunc("/api/ml/v1/cloud-disk/list", func(w http.ResponseWriter, r *http.Request) {
                w.Header().Set("Content-Type", "application/json")
                _, _ = w.Write([]byte(`{"code":0,"msg":"ok","data":{"files":[{"file_key":"/n.txt","file_name":"n.txt","is_dir":false,"size":5,"modified_at":1792815193,"cloud_file_status":null}],"next_page_token":""}}`))
        })
        srv := httptest.NewServer(mux)
        defer srv.Close()

        d := &InternAI{}
        d.MountPath = "/x"
        d.Addition.Token = "test-token"
        d.Addition.APIBase = srv.URL
        d.Addition.RootFolderPath = "/"
        if err := d.Init(context.Background()); err != nil {
                t.Fatalf("init failed: %v", err)
        }
        root := &model.Object{Name: "/", Path: "/", IsFolder: true}
        objs, err := d.List(context.Background(), root, model.ListArgs{})
        if err != nil {
                t.Fatalf("list with numeric modified_at failed: %v", err)
        }
        if len(objs) != 1 {
                t.Fatalf("expected 1 entry, got %d", len(objs))
        }
        want := time.Unix(1792815193, 0)
        if got := objs[0].ModTime(); !got.Equal(want) {
                t.Fatalf("modified = %v, want %v", got, want)
        }
}

func TestCleanKey(t *testing.T) {
        cases := map[string]string{
                "":            "/",
                "/":           "/",
                "a/b":         "/a/b",
                "/a//b/":      "/a/b",
                "\\a\\b":      "/a/b",
                "/a/../b":     "/b",
                "/docs/a.txt": "/docs/a.txt",
        }
        for in, want := range cases {
                if got := cleanKey(in); got != want {
                        t.Errorf("cleanKey(%q) = %q, want %q", in, got, want)
                }
        }
}
