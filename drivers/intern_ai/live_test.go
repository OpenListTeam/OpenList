package intern_ai

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
