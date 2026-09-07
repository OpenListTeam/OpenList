package handles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/tache"
	"github.com/gin-gonic/gin"
)

func contextWithUser(ctx context.Context, user *model.User) context.Context {
	return context.WithValue(ctx, conf.UserKey, user)
}

// buildPathRouter wires the by-path handlers with an injected user, mimicking
// what middlewares.Auth does, so we can assert permission scoping end-to-end.
func buildPathRouter(m *tache.Manager[*pathTestTask], user *model.User) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			ctx := c.Request.Context()
			//nolint:staticcheck // mirrors production context key usage
			c.Request = c.Request.WithContext(contextWithUser(ctx, user))
		}
		c.Next()
	})
	g := r.Group("/task/copy")
	g.POST("/delete_by_path", deleteByPath[*pathTestTask](m))
	g.POST("/cancel_by_path", cancelByPath[*pathTestTask](m))
	g.POST("/retry_by_path", retryByPath[*pathTestTask](m))
	return r
}

func postPath(t *testing.T, r *gin.Engine, url, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func respCode(out map[string]any) int {
	if v, ok := out["code"].(float64); ok {
		return int(v)
	}
	return -1
}

func respResult(t *testing.T, out map[string]any) TaskPathResult {
	t.Helper()
	data, ok := out["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object in response: %v", out)
	}
	matched, ok := data["matched"].(float64)
	if !ok {
		t.Fatalf("no matched in data: %v", data)
	}
	processed, ok := data["processed"].(float64)
	if !ok {
		t.Fatalf("no processed in data: %v", data)
	}
	return TaskPathResult{Matched: int(matched), Processed: int(processed)}
}

func TestDeleteByPathHTTPEmptyPathRejected(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	m := newPathManager(1)
	r := buildPathRouter(m, admin)

	_, out := postPath(t, r, "/task/copy/delete_by_path", `{"path":"   "}`)
	if got := respCode(out); got != 400 {
		t.Fatalf("empty path code = %d, want 400 (%v)", got, out)
	}
}

func TestDeleteByPathHTTPImplicitRootRejected(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	m := newPathManager(1)
	r := buildPathRouter(m, admin)

	for _, body := range []string{`{"path":"."}`, `{"path":"//"}`, `{"path":"./"}`} {
		_, out := postPath(t, r, "/task/copy/delete_by_path", body)
		if got := respCode(out); got != 400 {
			t.Fatalf("implicit root %s code = %d, want 400 (%v)", body, got, out)
		}
	}
}

func TestDeleteByPathHTTPNoUserRejected(t *testing.T) {
	ensureTaskTestConf()
	m := newPathManager(1)
	r := buildPathRouter(m, nil)

	_, out := postPath(t, r, "/task/copy/delete_by_path", `{"path":"/x"}`)
	if got := respCode(out); got != 401 {
		t.Fatalf("missing user code = %d, want 401 (%v)", got, out)
	}
}

func TestDeleteByPathHTTPScopedToOwnerAndBasePath(t *testing.T) {
	ensureTaskTestConf()
	owner := &model.User{ID: 2, Role: model.GENERAL, BasePath: "/home/u2"}
	stranger := &model.User{ID: 3, Role: model.GENERAL, BasePath: "/home/u3"}
	m := newPathManager(1)

	// owner's task inside their base path -> should be deleted
	mine := addPathTask(m, owner, "", "/home/u2/docs/a", tache.StatePending)
	// another user's task under the same resolved path -> must survive
	notMine := addPathTask(m, stranger, "", "/home/u2/docs/b", tache.StatePending)
	// owner's task outside the requested prefix -> must survive
	outside := addPathTask(m, owner, "", "/home/u2/other/c", tache.StatePending)

	r := buildPathRouter(m, owner)
	// non-admin sends a relative path; it is joined onto their BasePath
	_, out := postPath(t, r, "/task/copy/delete_by_path", `{"path":"docs"}`)
	if got := respCode(out); got != 200 {
		t.Fatalf("code = %d (%v)", got, out)
	}
	if got := respResult(t, out); got != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("result = %+v, want matched=1 processed=1", got)
	}
	if _, ok := m.GetByID(mine.GetID()); ok {
		t.Fatal("owner task under prefix should be removed")
	}
	if _, ok := m.GetByID(notMine.GetID()); !ok {
		t.Fatal("SECURITY: another user's task was removed")
	}
	if _, ok := m.GetByID(outside.GetID()); !ok {
		t.Fatal("task outside prefix was removed")
	}
}

func TestDeleteByPathHTTPAdminNotEscapedByTraversal(t *testing.T) {
	ensureTaskTestConf()
	user := &model.User{ID: 4, Role: model.GENERAL, BasePath: "/home/u4"}
	m := newPathManager(1)

	// a task that lives outside the user's base path
	foreign := addPathTask(m, user, "", "/etc/secret/x", tache.StatePending)

	r := buildPathRouter(m, user)
	// attempt to escape the base path via traversal
	_, out := postPath(t, r, "/task/copy/delete_by_path", `{"path":"../../etc/secret"}`)
	code := respCode(out)
	if code == 200 {
		if got := respResult(t, out); got != (TaskPathResult{}) {
			t.Fatalf("SECURITY: traversal processed tasks outside base path: %+v", got)
		}
	}
	if _, ok := m.GetByID(foreign.GetID()); !ok {
		t.Fatal("SECURITY: task outside base path removed via path traversal")
	}
}

func TestCancelAndRetryByPathHTTPStateFiltering(t *testing.T) {
	ensureTaskTestConf()
	admin := &model.User{ID: 1, Role: model.ADMIN}
	m := newPathManager(1)

	pending := addPathTask(m, admin, "", "/p/pending", tache.StatePending)
	failed := addPathTask(m, admin, "", "/p/failed", tache.StateFailed)
	done := addPathTask(m, admin, "", "/p/done", tache.StateSucceeded)

	r := buildPathRouter(m, admin)

	_, out := postPath(t, r, "/task/copy/cancel_by_path", `{"path":"/p"}`)
	if got := respResult(t, out); got != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("cancel result = %+v", got)
	}
	if done.GetState() != tache.StateSucceeded {
		t.Fatalf("succeeded task altered by cancel: %v", done.GetState())
	}

	_, out = postPath(t, r, "/task/copy/retry_by_path", `{"path":"/p"}`)
	if got := respResult(t, out); got != (TaskPathResult{Matched: 1, Processed: 1}) {
		t.Fatalf("retry result = %+v", got)
	}
	if failed.GetState() != tache.StateWaitingRetry {
		t.Fatalf("failed task not queued for retry: %v", failed.GetState())
	}
	if st := pending.GetState(); st != tache.StateCanceling && st != tache.StateCanceled {
		t.Fatalf("pending task state after cancel = %v", st)
	}
}
