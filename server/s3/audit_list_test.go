package s3

import (
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
)

// auditServer starts the S3 API over a real Local storage mounted at /mp, with
// the same listing post-processing the production chain applies.
func auditServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	b, localRoot := setupMultipartBackend(t)
	srv := httptest.NewServer(fixListEncoding(gofakes3.New(b).Server()))
	t.Cleanup(srv.Close)
	return srv, localRoot
}

// auditWrite creates an object body directly in the Local storage root, so the
// listing walk sees exactly the keys the test asks for.
func auditWrite(t *testing.T, localRoot, key string) {
	t.Helper()
	p := filepath.Join(localRoot, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %q: %v", key, err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %q: %v", key, err)
	}
}

// auditTree creates every key and returns them sorted.
func auditTree(t *testing.T, srv *httptest.Server, localRoot string, keys ...string) []string {
	t.Helper()
	for _, k := range keys {
		auditWrite(t, localRoot, k)
	}
	out := append([]string(nil), keys...)
	sort.Strings(out)
	return out
}

// auditListResult is the subset of ListBucketResult a client decodes.
type auditListResult struct {
	Name          string `xml:"Name"`
	Prefix        string `xml:"Prefix"`
	Delimiter     string `xml:"Delimiter"`
	Marker        string `xml:"Marker"`
	StartAfter    string `xml:"StartAfter"`
	NextMarker    string `xml:"NextMarker"`
	MaxKeys       int    `xml:"MaxKeys"`
	KeyCount      int    `xml:"KeyCount"`
	EncodingType  string `xml:"EncodingType"`
	IsTruncated   bool   `xml:"IsTruncated"`
	NextContToken string `xml:"NextContinuationToken"`
	Contents      []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	CommonPrefixes []struct {
		Prefix string `xml:"Prefix"`
	} `xml:"CommonPrefixes"`
}

// decode applies the rule a client applies to an encoding-type=url listing:
// percent decoding, with "+" standing for a space.
func (r *auditListResult) decode(encoded bool) (keys, prefixes []string) {
	fix := func(s string) string {
		if !encoded {
			return s
		}
		u, err := url.QueryUnescape(s)
		if err != nil {
			return s
		}
		return u
	}
	for _, c := range r.Contents {
		keys = append(keys, fix(c.Key))
	}
	for _, p := range r.CommonPrefixes {
		prefixes = append(prefixes, fix(p.Prefix))
	}
	return keys, prefixes
}

func auditGet(t *testing.T, srv *httptest.Server, query string) *auditListResult {
	t.Helper()
	resp, err := http.Get(srv.URL + "/mp?" + query)
	if err != nil {
		t.Fatalf("GET ?%s: %v", query, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body for ?%s: %v", query, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET ?%s -> %d: %s", query, resp.StatusCode, body)
	}
	var out auditListResult
	if err := xml.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode listing for ?%s: %v (%s)", query, err, body)
	}
	return &out
}

// auditWalk follows the server's own cursor from an optional client marker the
// way aws-cli/rclone do, and returns every entry the pages reported in order.
func auditWalk(t *testing.T, srv *httptest.Server, prefix, delimiter string, maxKeys int, listType int, encoded bool, from string) []string {
	t.Helper()
	q := url.Values{}
	q.Set("prefix", prefix)
	q.Set("delimiter", delimiter)
	q.Set("max-keys", strconv.Itoa(maxKeys))
	if listType == 2 {
		q.Set("list-type", "2")
	}
	if encoded {
		q.Set("encoding-type", "url")
	}
	if from != "" {
		if listType == 2 {
			q.Set("start-after", from)
		} else {
			q.Set("marker", from)
		}
	}
	var out []string
	cursor := ""
	for page := 0; ; page++ {
		if page > 2000 {
			t.Fatalf("pagination did not terminate: prefix=%q delimiter=%q max-keys=%d start=%q", prefix, delimiter, maxKeys, from)
		}
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		if page > 0 {
			if listType == 2 {
				qq.Set("continuation-token", cursor)
				qq.Del("start-after")
			} else {
				qq.Set("marker", cursor)
			}
		}
		res := auditGet(t, srv, qq.Encode())
		keys, prefixes := res.decode(encoded)
		if page > 0 && len(keys)+len(prefixes) == 0 {
			t.Fatalf("page %d is empty after a truncated page: prefix=%q delimiter=%q max-keys=%d start=%q", page, prefix, delimiter, maxKeys, from)
		}
		out = append(out, keys...)
		out = append(out, prefixes...)
		if !res.IsTruncated {
			return out
		}
		if listType == 2 {
			if res.NextContToken == "" {
				t.Fatalf("truncated page without NextContinuationToken: prefix=%q delimiter=%q max-keys=%d", prefix, delimiter, maxKeys)
			}
			cursor = res.NextContToken
			continue
		}
		next := res.NextMarker
		if encoded {
			if u, err := url.QueryUnescape(next); err == nil {
				next = u
			}
		}
		if next == "" && len(keys) > 0 {
			next = keys[len(keys)-1]
		}
		if next == "" {
			t.Fatalf("truncated page without NextMarker: prefix=%q delimiter=%q max-keys=%d", prefix, delimiter, maxKeys)
		}
		if next == cursor {
			t.Fatalf("cursor did not advance (%q): prefix=%q delimiter=%q max-keys=%d", next, prefix, delimiter, maxKeys)
		}
		cursor = next
	}
}

// auditOracle is the listing S3 describes: keep the keys that carry the prefix
// and sort after the marker, then collapse each one at the first delimiter
// found after the prefix.
func auditOracle(keys []string, prefix, delimiter, after string) []string {
	set := map[string]bool{}
	for _, k := range keys {
		if !strings.HasPrefix(k, prefix) || k <= after {
			continue
		}
		rest := k[len(prefix):]
		if delimiter != "" {
			if at := strings.Index(rest, delimiter); at >= 0 {
				set[prefix+rest[:at+len(delimiter)]] = true
				continue
			}
		}
		set[k] = true
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func auditEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	sort.Strings(g)
	if strings.Join(g, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("%s:\n got = %q\nwant = %q", what, g, want)
	}
}

// A marker that points *inside* a common prefix is what aws-cli sends when the
// user passes --start-after with a key that lives in a "directory", and what
// rclone sends after it has listed part of one. The keys beyond the marker are
// still part of that prefix, so S3 answers with the same common prefix again;
// dropping it silently loses every one of those keys.
func TestListMarkerInsideCommonPrefixDoesNotDropKeys(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root, "a/1.txt", "a/b.txt", "b.txt", "zz/1.txt")

	for _, tc := range []struct {
		name  string
		list  int
		after string
	}{
		{name: "v1 marker inside prefix", list: 1, after: "a/1.txt"},
		{name: "v2 start-after inside prefix", list: 2, after: "a/1.txt"},
		{name: "v1 marker inside a deeper prefix", list: 1, after: "a/0"},
		{name: "v2 start-after before the last key of the prefix", list: 2, after: "a/0"},
	} {
		want := auditOracle(keys, "", "/", tc.after)
		got := auditWalk(t, srv, "", "/", 1000, tc.list, false, tc.after)
		auditEqual(t, tc.name, got, want)
	}

	// A marker sitting exactly on the prefix needs no re-report: the page that
	// ended on it already handed the client that prefix as its cursor, and
	// repeating it would make the cursor stand still.
	got := auditWalk(t, srv, "", "/", 1000, 1, false, "a/")
	auditEqual(t, "v1 marker equal to the prefix", got, []string{"b.txt", "zz/"})

	// Nothing beyond the marker means no prefix at all: the last key of the
	// prefix is the marker, so the "directory" is fully listed already.
	got = auditWalk(t, srv, "", "/", 1000, 1, false, "a/b.txt")
	auditEqual(t, "v1 marker on the last key of the prefix", got, []string{"b.txt", "zz/"})
}

// listingTree is the fixture the prefix/delimiter matrix runs on: keys that are
// also prefixes, prefixes made only of a synthetic "directory", a prefix that
// does not end in the delimiter, and keys whose neighbours differ by one byte.
var listingTree = []string{
	"a.txt", "a-b.txt", "a/1.txt", "a/b/2.txt", "a/b.txt", "a/b/c/3.txt",
	"ab/x.txt", "b/1.txt", "b/10.txt", "b/2.txt", "c.txt", "c/d.txt",
}

func TestListPrefixDelimiterMatrix(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root, listingTree...)

	prefixes := []string{
		"", "a", "a/", "a/b", "a/b/", "a/b.txt", "a/b/c", "a/1.txt", "ab", "ab/",
		"b", "c", "c/", "nope", "a/x",
	}
	delimiters := []string{"", "/"}
	maxKeys := []int{1, 2, 3, 1000}

	for _, listType := range []int{1, 2} {
		for _, encoded := range []bool{false, true} {
			for _, delimiter := range delimiters {
				for _, prefix := range prefixes {
					want := auditOracle(keys, prefix, delimiter, "")
					for _, mk := range maxKeys {
						what := "list=" + strconv.Itoa(listType) + " encoded=" + strconv.FormatBool(encoded) +
							" prefix=" + strconv.Quote(prefix) + " delimiter=" + strconv.Quote(delimiter) +
							" max-keys=" + strconv.Itoa(mk)
						got := auditWalk(t, srv, prefix, delimiter, mk, listType, encoded, "")
						auditEqual(t, what, got, want)
					}
				}
			}
		}
	}
}

func TestListGroupsByNonSlashDelimiterEndToEnd(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root,
		"a:b", "a:c", "a:d/x", "c:d/e.txt", "plain.txt", "q/x:y", "x:y:z")

	for _, prefix := range []string{"", "a", "a:", "c", "q/", "q/x"} {
		want := auditOracle(keys, prefix, ":", "")
		for _, mk := range []int{1, 2, 3, 1000} {
			got := auditWalk(t, srv, prefix, ":", mk, 1, false, "")
			auditEqual(t, "prefix="+strconv.Quote(prefix)+" max-keys="+strconv.Itoa(mk), got, want)
		}
	}
}

// Every key the listing returned is a marker a client may resume from, and so
// is a key that no longer exists (deleted between pages) or one that never
// did. None of them may make the rest of the bucket disappear.
func TestListMarkerAtEveryKeyLosesNothing(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root, listingTree...)

	markers := append([]string{"", "a/0", "b/15", "c!"}, keys...)
	for _, listType := range []int{1, 2} {
		for _, delimiter := range []string{"", "/"} {
			for _, marker := range markers {
				want := auditOracle(keys, "", delimiter, marker)
				if len(marker) > 0 && strings.HasSuffix(marker, "/") {
					// A marker that is itself a common prefix was handed to the
					// client by the page that ended on it, so it is skipped
					// instead of repeated; compare against the same rule.
					want = auditOracle(keys, "", delimiter, marker)
					filtered := want[:0]
					for _, w := range want {
						if w != marker {
							filtered = append(filtered, w)
						}
					}
					want = filtered
				}
				got := auditWalk(t, srv, "", delimiter, 2, listType, false, marker)
				auditEqual(t, "list="+strconv.Itoa(listType)+" delimiter="+strconv.Quote(delimiter)+
					" marker="+strconv.Quote(marker), got, want)
			}
		}
	}
}

func TestListContinuationTokenRoundTripAndStaleToken(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root,
		"a+plus.txt", "sp ace.txt", "pct%.txt", "ü.txt", "amp&.txt", "q?mark.txt",
		"d+ir/1.txt", "d+ir/2.txt", "e/1.txt", "e/2.txt")

	// The token the server hands out has to come back to the same position when
	// the listing is URL encoded, which is where "+" and "%" would be mangled.
	for _, encoded := range []bool{false, true} {
		for _, delimiter := range []string{"", "/"} {
			want := auditOracle(keys, "", delimiter, "")
			got := auditWalk(t, srv, "", delimiter, 2, 2, encoded, "")
			auditEqual(t, "encoded="+strconv.FormatBool(encoded)+" delimiter="+strconv.Quote(delimiter), got, want)
		}
	}

	// A page two token names a key; deleting that key (or the page's last entry)
	// must not make the rest unreachable.
	first := auditGet(t, srv, "list-type=2&max-keys=2")
	if !first.IsTruncated || first.NextContToken == "" {
		t.Fatalf("first page: truncated=%v token=%q", first.IsTruncated, first.NextContToken)
	}
	pageKeys, _ := first.decode(false)
	if len(pageKeys) != 2 {
		t.Fatalf("first page returned %d keys, want 2", len(pageKeys))
	}
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(pageKeys[len(pageKeys)-1]))); err != nil {
		t.Fatalf("delete %q: %v", pageKeys[len(pageKeys)-1], err)
	}
	gone := pageKeys[len(pageKeys)-1]
	rest := []string{"e/1.txt", "e/2.txt", "pct%.txt", "q?mark.txt", "sp ace.txt", "ü.txt"}
	for _, k := range keys {
		if k > gone {
			rest = append(rest, k)
		}
	}
	second := auditGet(t, srv, "list-type=2&max-keys=1000&continuation-token="+url.QueryEscape(first.NextContToken))
	got, _ := second.decode(false)
	sort.Strings(got)
	auditEqual(t, "resume after a deleted key", got, auditOracle(rest, "", "", ""))
}

func TestListMaxKeysZeroAndAboveTheCap(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root, listingTree...)

	// max-keys=0 asks for no entries. It must not claim truncation, because a
	// client that loops while IsTruncated is set would never make progress.
	for _, listType := range []int{1, 2} {
		q := "max-keys=0"
		if listType == 2 {
			q += "&list-type=2"
		}
		res := auditGet(t, srv, q)
		if res.IsTruncated {
			t.Errorf("list=%d max-keys=0: IsTruncated = true", listType)
		}
		if res.NextContToken != "" || res.NextMarker != "" {
			t.Errorf("list=%d max-keys=0: cursor returned (%q/%q)", listType, res.NextMarker, res.NextContToken)
		}
		k, p := res.decode(false)
		if len(k)+len(p) != 0 {
			t.Errorf("list=%d max-keys=0: %d keys and %d prefixes returned", listType, len(k), len(p))
		}
	}

	// A max-keys above the page cap is clamped, and the whole listing is still
	// reachable page by page.
	res := auditGet(t, srv, "list-type=2&max-keys=5000")
	if n := len(res.Contents) + len(res.CommonPrefixes); n > 1000 {
		t.Errorf("max-keys=5000 returned %d entries, want at most 1000", n)
	}
	auditEqual(t, "max-keys=5000", auditWalk(t, srv, "", "", 5000, 2, false, ""), auditOracle(keys, "", "", ""))
}

func TestListEncodingTypeRoundTrip(t *testing.T) {
	srv, root := auditServer(t)
	keys := auditTree(t, srv, root,
		"a+plus.txt", "sp ace.txt", "pct%.txt", "ü.txt", "amp&.txt", "q?mark.txt", "hash#.txt",
		"d+ir/inner.txt", "dir with space/x.txt", "sl/ash/inside.txt")

	// Keys, prefixes and the prefixes of groups all have to decode back to the
	// value the client asked about; "+" must not be read as a space, and a "%"
	// in the key must survive.
	for _, query := range []string{
		"list-type=2&encoding-type=url&delimiter=%2F",
		"list-type=2&encoding-type=url",
		"encoding-type=url&delimiter=%2F",
		"list-type=2&encoding-type=url&delimiter=%2F&prefix=d",
		"list-type=2&encoding-type=url&delimiter=%2F&prefix=d%2B",
		"list-type=2&encoding-type=url&delimiter=%2F&prefix=dir%20with",
	} {
		res := auditGet(t, srv, query)
		gotKeys, gotPrefixes := res.decode(true)
		delimiter := "/"
		if !strings.Contains(query, "delimiter") {
			delimiter = ""
		}
		prefix := ""
		if v, err := url.ParseQuery(query); err == nil {
			prefix = v.Get("prefix")
		}
		want := auditOracle(keys, prefix, delimiter, "")
		var got []string
		got = append(got, gotKeys...)
		got = append(got, gotPrefixes...)
		auditEqual(t, query, got, want)
	}
}
