package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	odriver "github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/go-resty/resty/v2"
)

func TestShouldUseAccurateMtime(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		token      string
		entryCount int
		want       bool
		wantReason string
	}{
		{name: "disabled", enabled: false, token: "token", entryCount: 2, want: false, wantReason: "disabled"},
		{name: "missing token", enabled: true, token: "   ", entryCount: 2, want: false, wantReason: "missing_token"},
		{name: "over limit", enabled: true, token: "token", entryCount: 201, want: false, wantReason: "entry_limit"},
		{name: "enabled", enabled: true, token: "token", entryCount: 200, want: true, wantReason: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := shouldUseAccurateMtime(tc.enabled, tc.token, tc.entryCount)
			if got != tc.want || reason != tc.wantReason {
				t.Fatalf("got (%v, %q), want (%v, %q)", got, reason, tc.want, tc.wantReason)
			}
		})
	}
}

func TestCollectMtimePaths(t *testing.T) {
	contents := collectMtimePaths("/docs", []Object{{Path: "docs/a.md"}, {Path: "/docs/b.md"}}, nil)
	if len(contents) != 2 || contents[0] != "/docs/a.md" || contents[1] != "/docs/b.md" {
		t.Fatalf("unexpected contents paths: %#v", contents)
	}

	tree := collectMtimePaths("/docs/sub", nil, []TreeObjResp{{TreeObjReq: TreeObjReq{Path: "a.md"}}, {TreeObjReq: TreeObjReq{Path: "dir/b.md"}}})
	if len(tree) != 2 || tree[0] != "/docs/sub/a.md" || tree[1] != "/docs/sub/dir/b.md" {
		t.Fatalf("unexpected tree paths: %#v", tree)
	}
}

func TestApplyModifiedTimesPreservesLegacyCreateTime(t *testing.T) {
	obj := &model.Object{Name: "a.md", Path: "/docs/a.md", Modified: githubZeroTime, Ctime: githubZeroTime}
	other := &model.Object{Name: "b.md", Path: "/docs/b.md", Modified: githubZeroTime, Ctime: githubZeroTime}
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)

	applyModifiedTimes([]model.Obj{obj, other}, map[string]time.Time{"/docs/a.md": stamp})

	if !obj.Modified.Equal(stamp) {
		t.Fatalf("modified not updated: %v", obj.Modified)
	}
	if !obj.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("created should stay legacy zero time: %v", obj.CreateTime())
	}
	if !other.Modified.Equal(githubZeroTime) {
		t.Fatalf("unmatched path should stay zero time: %v", other.Modified)
	}
}

func TestDriverInfoIncludesAccurateModifiedTimeDefault(t *testing.T) {
	info := op.GetDriverInfoMap()["GitHub API"]
	var found bool
	for _, item := range info.Additional {
		if item.Name != "accurate_modified_time" {
			continue
		}
		found = true
		if item.Default != "false" {
			t.Fatalf("unexpected default: %q", item.Default)
		}
		if !strings.Contains(item.Help, "Best-effort") {
			t.Fatalf("unexpected help: %q", item.Help)
		}
	}
	if !found {
		t.Fatal("accurate_modified_time item not registered")
	}
}

func TestBuildMtimeBatchQueryIncludesCommitAndTagPaths(t *testing.T) {
	query, aliasToPath := buildMtimeBatchQuery("owner", "repo", "release", []string{"/docs/a.md", "/docs/dir"})

	if aliasToPath["p0"] != "/docs/a.md" || aliasToPath["p1"] != "/docs/dir" {
		t.Fatalf("unexpected alias map: %#v", aliasToPath)
	}

	for _, want := range []string{
		`repository(owner: "owner", name: "repo")`,
		`object(expression: "release")`,
		`... on Commit {`,
		`oid`,
		`... on Tag {`,
		`target {`,
		`p0: history(first: 1, path: "docs/a.md")`,
		`p1: history(first: 1, path: "docs/dir")`,
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
}

func TestParseMtimeBatchResultReturnsStableCommitAndHandlesAliasLoss(t *testing.T) {
	body := []byte(`{
		"data": {
			"repository": {
				"refTarget": {
					"__typename": "Tag",
					"target": {
						"__typename": "Commit",
						"oid": "abc123",
						"p0": {"nodes": [{"committedDate": "2025-12-22T04:52:41Z"}]},
						"p1": {"nodes": []}
					}
				}
			}
		}
	}`)

	commitExpr, got, err := parseMtimeBatchResult(body, map[string]string{"p0": "/docs/a.md", "p1": "/docs/b.md", "p2": "/docs/c.md"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if commitExpr != "abc123" {
		t.Fatalf("expected resolved commit expr, got %q", commitExpr)
	}
	if _, ok := got["/docs/a.md"]; !ok {
		t.Fatalf("expected p0 timestamp in %#v", got)
	}
	if _, ok := got["/docs/b.md"]; ok {
		t.Fatalf("empty history should not backfill: %#v", got)
	}
	if _, ok := got["/docs/c.md"]; ok {
		t.Fatalf("missing alias should be ignored: %#v", got)
	}
}

func TestParseMtimeBatchResultRejectsTopLevelErrors(t *testing.T) {
	_, _, err := parseMtimeBatchResult([]byte(`{"errors":[{"message":"rate limited"}]}`), map[string]string{"p0": "/docs/a.md"})
	if err == nil {
		t.Fatal("expected top-level GraphQL errors to fail the whole batch")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newGithubTestDriver(rt roundTripFunc, token string, enabled bool) *Github {
	return &Github{
		Storage: model.Storage{MountPath: "/github-test", CacheExpiration: 10},
		Addition: Addition{
			RootPath:             odriver.RootPath{RootFolderPath: "/"},
			Token:                token,
			Owner:                "owner",
			Repo:                 "repo",
			Ref:                  "main",
			AccurateModifiedTime: enabled,
		},
		client: resty.New().SetTransport(rt),
	}
}

func newJSONResponse(status int, headers map[string]string, body string) *http.Response {
	h := make(http.Header)
	for key, value := range headers {
		h.Set(key, value)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(data)
}

func graphQLQueryFromRequest(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read graphql request body: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode graphql request body: %v", err)
	}
	query := payload["query"]
	if query == "" {
		t.Fatalf("graphql request missing query: %s", string(body))
	}
	return query
}

func newContentsPayload(t *testing.T, entries []Object) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"type":    "dir",
		"sha":     "tree-sha",
		"entries": entries,
	})
}

func newTreePayload(t *testing.T, sha string, trees []TreeObjResp) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sha":       sha,
		"truncated": false,
		"tree":      trees,
	})
}

func newCommitGraphQLPayload(t *testing.T, oid string, histories map[string][]string) string {
	t.Helper()
	refTarget := map[string]any{
		"__typename": "Commit",
		"oid":        oid,
	}
	for alias, dates := range histories {
		nodes := make([]map[string]string, 0, len(dates))
		for _, date := range dates {
			nodes = append(nodes, map[string]string{"committedDate": date})
		}
		refTarget[alias] = map[string]any{"nodes": nodes}
	}
	return mustJSON(t, map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"refTarget": refTarget,
			},
		},
	})
}

func newSequentialEntries(count, width int) []Object {
	entries := make([]Object, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("%0*d.md", width, i)
		entries = append(entries, Object{
			Name: name,
			Path: "docs/" + name,
			Type: "file",
			Size: 1,
		})
	}
	return entries
}

func newAliasHistories(count int, stamp string) map[string][]string {
	histories := make(map[string][]string, count)
	for i := 0; i < count; i++ {
		histories[fmt.Sprintf("p%d", i)] = []string{stamp}
	}
	return histories
}

func mustObject(t *testing.T, obj model.Obj) *model.Object {
	t.Helper()
	raw, ok := model.UnwrapObjName(obj).(*model.Object)
	if !ok {
		t.Fatalf("unexpected obj type %T", obj)
	}
	return raw
}

func TestListAppliesAccurateModifiedTimeAndKeepsLegacyCreated(t *testing.T) {
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	entries := []Object{
		{Name: "a.md", Path: "docs/a.md", Type: "file", Size: 1},
		{Name: "b.md", Path: "docs/b.md", Type: "file", Size: 1},
	}
	graphqlCalls := 0
	queries := make([]string, 0, 1)
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			queries = append(queries, graphQLQueryFromRequest(t, r))
			return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "42"}, newCommitGraphQLPayload(t, "abc123", map[string][]string{
				"p0": {stamp.Format(time.RFC3339)},
				"p1": {},
			})), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 1 {
		t.Fatalf("expected one GraphQL batch, got %d", graphqlCalls)
	}
	if len(queries) != 1 || !strings.Contains(queries[0], `object(expression: "main")`) {
		t.Fatalf("expected first batch to use ref expression, got %q", queries)
	}
	if len(objs) != 2 {
		t.Fatalf("expected two objects, got %d", len(objs))
	}
	first := mustObject(t, objs[0])
	second := mustObject(t, objs[1])
	if !first.ModTime().Equal(stamp) {
		t.Fatalf("expected accurate modified time, got %v", first.ModTime())
	}
	if !first.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("created should stay legacy zero time: %v", first.CreateTime())
	}
	if !second.ModTime().Equal(githubZeroTime) {
		t.Fatalf("unmatched entry should keep zero modified time: %v", second.ModTime())
	}
	if !second.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("created should stay legacy zero time: %v", second.CreateTime())
	}
}

func TestListKeepsLegacyBehaviorWhenAccurateMtimeDisabled(t *testing.T) {
	entries := []Object{{Name: "a.md", Path: "docs/a.md", Type: "file", Size: 1}}
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "42"}, newCommitGraphQLPayload(t, "abc123", newAliasHistories(1, time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC).Format(time.RFC3339)))), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", false)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 0 {
		t.Fatalf("disabled mode should make zero GraphQL calls, got %d", graphqlCalls)
	}
	first := mustObject(t, objs[0])
	if !first.ModTime().Equal(githubZeroTime) || !first.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("disabled mode should preserve legacy timestamps: mod=%v create=%v", first.ModTime(), first.CreateTime())
	}
}

func TestListStopsBeforeGraphQLForMissingTokenAndEntryLimit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		token   string
		entries int
	}{
		{name: "missing token", token: "", entries: 1},
		{name: "entry limit", token: "token", entries: 201},
	} {
		t.Run(tc.name, func(t *testing.T) {
			graphqlCalls := 0
			payload := newContentsPayload(t, newSequentialEntries(tc.entries, 3))
			drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
					return newJSONResponse(http.StatusOK, nil, payload), nil
				case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
					graphqlCalls++
					return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "42"}, newCommitGraphQLPayload(t, "abc123", newAliasHistories(1, time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC).Format(time.RFC3339)))), nil
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}), tc.token, true)

			objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if graphqlCalls != 0 {
				t.Fatalf("expected zero GraphQL calls, got %d", graphqlCalls)
			}
			for _, obj := range objs {
				raw := mustObject(t, obj)
				if !raw.ModTime().Equal(githubZeroTime) || !raw.CreateTime().Equal(githubZeroTime) {
					t.Fatalf("legacy timestamps should be preserved: mod=%v create=%v", raw.ModTime(), raw.CreateTime())
				}
			}
		})
	}
}

func TestListStopsAfterFirstFailedBatchAndKeepsRemainingZeroTime(t *testing.T) {
	entries := newSequentialEntries(51, 2)
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusUnauthorized, map[string]string{"X-Ratelimit-Remaining": "41"}, `{"message":"bad credentials"}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 1 {
		t.Fatalf("expected stop after first failed batch, got %d calls", graphqlCalls)
	}
	for _, obj := range objs {
		raw := mustObject(t, obj)
		if !raw.ModTime().Equal(githubZeroTime) {
			t.Fatalf("failed batch should keep zero time, got %v", raw.ModTime())
		}
		if !raw.CreateTime().Equal(githubZeroTime) {
			t.Fatalf("created should stay zero time, got %v", raw.CreateTime())
		}
	}
}

func TestListStopsAfterSecondBatchFailureAndKeepsFirstBatchBackfill(t *testing.T) {
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	entries := newSequentialEntries(51, 2)
	graphqlCalls := 0
	queries := make([]string, 0, 2)
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			queries = append(queries, graphQLQueryFromRequest(t, r))
			if graphqlCalls == 1 {
				return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "41"}, newCommitGraphQLPayload(t, "abc123", newAliasHistories(50, stamp.Format(time.RFC3339)))), nil
			}
			return newJSONResponse(http.StatusUnauthorized, map[string]string{"X-Ratelimit-Remaining": "41"}, `{"message":"bad credentials"}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 2 {
		t.Fatalf("expected two GraphQL calls, got %d", graphqlCalls)
	}
	if len(queries) != 2 {
		t.Fatalf("expected two captured queries, got %d", len(queries))
	}
	if !strings.Contains(queries[0], `object(expression: "main")`) {
		t.Fatalf("expected first batch to use ref expression, got %q", queries[0])
	}
	if !strings.Contains(queries[1], `object(expression: "abc123")`) {
		t.Fatalf("expected second batch to reuse resolved commit oid, got %q", queries[1])
	}
	for i, obj := range objs {
		raw := mustObject(t, obj)
		if i < 50 {
			if !raw.ModTime().Equal(stamp) {
				t.Fatalf("first batch should stay backfilled at index %d: %v", i, raw.ModTime())
			}
			if !raw.CreateTime().Equal(githubZeroTime) {
				t.Fatalf("created should stay zero time at index %d: %v", i, raw.CreateTime())
			}
			continue
		}
		if !raw.ModTime().Equal(githubZeroTime) {
			t.Fatalf("second batch failure should keep tail zero time, got %v", raw.ModTime())
		}
		if !raw.CreateTime().Equal(githubZeroTime) {
			t.Fatalf("created should stay zero time at tail: %v", raw.CreateTime())
		}
	}
}

func TestListStopsWhenRateLimitRemainingIsZero(t *testing.T) {
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	entries := newSequentialEntries(51, 2)
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "0"}, newCommitGraphQLPayload(t, "abc123", newAliasHistories(50, stamp.Format(time.RFC3339)))), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 1 {
		t.Fatalf("rate-limit header should stop remaining batches, got %d calls", graphqlCalls)
	}
	for i, obj := range objs {
		raw := mustObject(t, obj)
		if i < 50 {
			if !raw.ModTime().Equal(stamp) {
				t.Fatalf("first batch should stay backfilled at index %d: %v", i, raw.ModTime())
			}
			continue
		}
		if !raw.ModTime().Equal(githubZeroTime) {
			t.Fatalf("remaining entries should keep zero time, got %v", raw.ModTime())
		}
	}
}

func TestListUsesFourGraphQLBatchesAtEntryLimit(t *testing.T) {
	entries := newSequentialEntries(200, 3)
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "42"}, newCommitGraphQLPayload(t, "abc123", nil)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	_, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 4 {
		t.Fatalf("200 entries should use exactly 4 batches, got %d", graphqlCalls)
	}
}

func TestListKeepsTreeFallbackOnLegacyPath(t *testing.T) {
	entries := make([]Object, 0, 1000)
	for i := 0; i < 1000; i++ {
		name := fmt.Sprintf("dir-%d", i)
		entries = append(entries, Object{Name: name, Path: "docs/" + name, Type: "dir"})
	}
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			return newJSONResponse(http.StatusOK, nil, newTreePayload(t, "tree-sha", []TreeObjResp{{TreeObjReq: TreeObjReq{Path: "child.md", Mode: "100644", Type: "blob", Sha: "blob-sha"}, Size: 1, URL: "https://example.invalid/blob"}})), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "42"}, newCommitGraphQLPayload(t, "abc123", nil)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("unexpected tree fallback result length: %d", len(objs))
	}
	first := mustObject(t, objs[0])
	if first.GetPath() != "/child.md" {
		t.Fatalf("unexpected tree fallback path: %s", first.GetPath())
	}
	if !first.ModTime().Equal(githubZeroTime) || !first.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("tree fallback should preserve legacy timestamps: mod=%v create=%v", first.ModTime(), first.CreateTime())
	}
	if graphqlCalls != 0 {
		t.Fatalf("tree fallback should skip GraphQL, got %d calls", graphqlCalls)
	}
}

func TestOpListCacheHitDoesNotRepeatGraphQL(t *testing.T) {
	op.Cache.ClearAll()
	defer op.Cache.ClearAll()
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, nil, newContentsPayload(t, []Object{{Name: "a.md", Path: "a.md", Type: "file", Size: 1}})), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, map[string]string{"X-Ratelimit-Remaining": "40"}, newCommitGraphQLPayload(t, "abc123", map[string][]string{"p0": {stamp.Format(time.RFC3339)}})), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	first, err := op.List(context.Background(), drv, "/", model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected first list error: %v", err)
	}
	second, err := op.List(context.Background(), drv, "/", model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected second list error: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected cached results: first=%d second=%d", len(first), len(second))
	}
	if graphqlCalls != 1 {
		t.Fatalf("expected one GraphQL call across cached lists, got %d", graphqlCalls)
	}
	if !mustObject(t, first[0]).ModTime().Equal(stamp) {
		t.Fatalf("expected first list to include backfilled modified time, got %v", mustObject(t, first[0]).ModTime())
	}
	if !mustObject(t, second[0]).ModTime().Equal(stamp) {
		t.Fatalf("expected cached list to retain modified time, got %v", mustObject(t, second[0]).ModTime())
	}
}
