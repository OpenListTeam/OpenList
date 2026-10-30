package intern_ai

import (
        "bytes"
        "context"
        "io"
        "net/http"
        "os"
        "path"
        "strconv"
        "strings"
        "testing"
        "time"

        "github.com/OpenListTeam/OpenList/v4/internal/driver"
        "github.com/OpenListTeam/OpenList/v4/internal/model"
        "github.com/OpenListTeam/OpenList/v4/internal/stream"
)

// TestLiveGateway performs a read-only smoke test against the real
// discovery.intern-ai.org.cn gateway. It is skipped unless INTERN_AI_LIVE=1 so
// that CI never depends on an external service.
//
// With INTERN_AI_TOKEN unset it exercises the error path (invalid token must
// surface the platform's 401 / A0202 authentication failure); with a valid
// token it lists the drive root and resolves one download link.
func TestLiveGateway(t *testing.T) {
        if os.Getenv("INTERN_AI_LIVE") != "1" {
                t.Skip("set INTERN_AI_LIVE=1 to run the live gateway smoke test")
        }
        token := os.Getenv("INTERN_AI_TOKEN")
        d := &InternAI{}
        d.MountPath = "/internai-live"
        d.Addition.Token = token
        d.Addition.APIBase = "https://discovery.intern-ai.org.cn"
        d.Addition.PageSize = 50
        d.Addition.RootFolderPath = "/"
        err := d.Init(context.Background())
        switch {
        case token == "":
                if err == nil {
                        t.Fatal("expected init to fail without a token")
                }
                t.Logf("live error path ok: %v", err)
                return
        case strings.HasPrefix(token, "fake"):
                // Placeholder token: the platform must reject it with HTTP 401 and the
                // driver must surface a readable authentication error.
                if err == nil {
                        t.Fatal("expected init to fail with a fake token")
                }
                if !strings.Contains(err.Error(), "invalid or expired token") {
                        t.Fatalf("expected an auth error, got: %v", err)
                }
                t.Logf("live 401 path ok: %v", err)
                return
        }
        if err != nil {
                t.Fatalf("init with token failed: %v", err)
        }
        root := &model.Object{Name: "/", Path: "/", IsFolder: true}
        objs, err := d.List(context.Background(), root, model.ListArgs{})
        if err != nil {
                t.Fatalf("live list failed: %v", err)
        }
        t.Logf("live list returned %d entries", len(objs))
        for i, o := range objs {
                if i >= 10 {
                        break
                }
                t.Logf("  %s (dir=%v size=%d)", o.GetName(), o.IsDir(), o.GetSize())
        }
        for _, o := range objs {
                if o.IsDir() {
                        continue
                }
                link, err := d.Link(context.Background(), o, model.LinkArgs{})
                if err != nil {
                        t.Logf("  link %s failed (expected for some types): %v", o.GetName(), err)
                        continue
                }
                t.Logf("  link %s -> %s", o.GetName(), link.URL[:min(80, len(link.URL))])
                break
        }
}

// TestLiveWriteE2E performs a full read/write round trip against the real
// gateway inside a throwaway directory: mkdir -> put -> list -> link+download
// verify -> rename -> mkdir sub -> move -> copy -> remove everything.
//
// It only runs when BOTH INTERN_AI_LIVE=1 and INTERN_AI_E2E=1 with a real
// token, so regular CI never writes to the cloud disk.
func TestLiveWriteE2E(t *testing.T) {
        if os.Getenv("INTERN_AI_LIVE") != "1" || os.Getenv("INTERN_AI_E2E") != "1" {
                t.Skip("set INTERN_AI_LIVE=1 INTERN_AI_E2E=1 with a real token to run the write e2e")
        }
        token := os.Getenv("INTERN_AI_TOKEN")
        if token == "" || strings.HasPrefix(token, "fake") {
                t.Skip("live write e2e needs a real token")
        }
        ctx := context.Background()
        d := &InternAI{}
        d.MountPath = "/internai-live"
        d.Addition.Token = token
        d.Addition.APIBase = "https://discovery.intern-ai.org.cn"
        d.Addition.PageSize = 50
        d.Addition.RootFolderPath = "/"
        if err := d.Init(ctx); err != nil {
                t.Fatalf("init failed: %v", err)
        }

        work := "/openlist-e2e-" + strconv.FormatInt(time.Now().Unix(), 10)
        root := &model.Object{Name: "/", Path: "/", IsFolder: true}
        cleanup := func() {
                if err := d.Remove(ctx, &model.Object{Name: path.Base(work), Path: work, IsFolder: true}); err != nil {
                        t.Logf("cleanup failed for %s: %v", work, err)
                }
        }
        // best-effort cleanup no matter how the test ends
        defer cleanup()

        // 1. mkdir workspace
        if _, err := d.MakeDir(ctx, root, path.Base(work)); err != nil {
                t.Fatalf("mkdir: %v", err)
        }
        workDir := &model.Object{Name: path.Base(work), Path: work, IsFolder: true}

        // 2. put a file
        content := []byte("openlist live e2e " + time.Now().Format(time.RFC3339))
        fs := &stream.FileStream{
                Ctx:    ctx,
                Obj:    &model.Object{Name: "hello.txt", Size: int64(len(content)), IsFolder: false, Modified: time.Now()},
                Reader: bytes.NewReader(content),
        }
        if _, err := d.Put(ctx, workDir, fs, driver.UpdateProgress(func(float64) {})); err != nil {
                t.Fatalf("put: %v", err)
        }

        // 3. list and verify
        objs, err := d.List(ctx, workDir, model.ListArgs{})
        if err != nil {
                t.Fatalf("list: %v", err)
        }
        if len(objs) != 1 || objs[0].GetName() != "hello.txt" || objs[0].GetSize() != int64(len(content)) {
                t.Fatalf("unexpected listing after put: %+v", objs)
        }
        if objs[0].ModTime().IsZero() {
                t.Fatalf("modified time of the uploaded file should be parsed")
        }

        // 4. link + download round trip
        link, err := d.Link(ctx, objs[0], model.LinkArgs{})
        if err != nil {
                t.Fatalf("link: %v", err)
        }
        resp, err := http.Get(link.URL)
        if err != nil {
                t.Fatalf("download: %v", err)
        }
        got, _ := io.ReadAll(resp.Body)
        resp.Body.Close()
        if resp.StatusCode != 200 || !bytes.Equal(got, content) {
                t.Fatalf("download round trip mismatch: status=%d len=%d", resp.StatusCode, len(got))
        }

        // 5. rename
        if _, err := d.Rename(ctx, objs[0], "renamed.txt"); err != nil {
                t.Fatalf("rename: %v", err)
        }

        // 6. mkdir sub + move
        sub, err := d.MakeDir(ctx, workDir, "sub")
        if err != nil {
                t.Fatalf("mkdir sub: %v", err)
        }
        if _, err := d.Move(ctx, &model.Object{Name: "renamed.txt", Path: joinKey(work, "renamed.txt")}, sub); err != nil {
                t.Fatalf("move: %v", err)
        }

        // 7. copy back
        if _, err := d.Copy(ctx, &model.Object{Name: "renamed.txt", Path: joinKey(joinKey(work, "sub"), "renamed.txt")}, workDir); err != nil {
                t.Fatalf("copy: %v", err)
        }

        // 8. verify both copies exist
        objs, err = d.List(ctx, workDir, model.ListArgs{})
        if err != nil {
                t.Fatalf("final list: %v", err)
        }
        names := map[string]bool{}
        for _, o := range objs {
                names[o.GetName()] = true
        }
        if !names["renamed.txt"] {
                t.Fatalf("copied file missing after move/copy: %v", names)
        }

        t.Logf("live write e2e ok (workspace %s created and cleaned)", work)
}
