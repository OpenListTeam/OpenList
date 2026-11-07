package s3

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"os"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// The copy source of a CopyObject travels in a header, and Signature V4 only
// authenticates the headers listed in SignedHeaders. bucketAuthHandler let every
// header-signed request use X-Amz-Copy-Source on the assumption that a header
// "covers every header it sends", which is not true: gofakes3 canonicalises the
// headers named in SignedHeaders and nothing else, so a captured or replayed
// signed request can grow a copy source the signer never saw and make the
// gateway move another bucket's object into a key the caller controls.
//
// AWS refuses that request ("There were headers present in the request which
// were not signed"), and so must this gateway.

const copyAuditRule = `[{"access_key":"leaky-key","secret_key":"leaky-secret",` +
	`"buckets":["src-bucket","dst-bucket"],"readonly":false}]`

func hmacSHA(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}

// signV4Header installs a SigV4 Authorization header that covers exactly the
// named headers, with the payload declared as UNSIGNED-PAYLOAD (which is what a
// presigned-style client and many hand-rolled ones send).
func signV4Header(t *testing.T, req *http.Request, accessKey, secretKey string, names []string) {
	t.Helper()
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	const region, service = "us-east-1", "s3"

	req.Header.Set("X-Amz-Date", stamp)
	req.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")

	sorted := append([]string(nil), names...)
	for i := range sorted {
		sorted[i] = strings.ToLower(sorted[i])
	}
	sort.Strings(sorted)

	var canonical, signed strings.Builder
	for i, name := range sorted {
		value := req.Header.Get(name)
		if name == "host" {
			value = req.Host
		}
		canonical.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
		if i > 0 {
			signed.WriteString(";")
		}
		signed.WriteString(name)
	}
	query := strings.ReplaceAll(req.URL.Query().Encode(), "+", "%20")
	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		query,
		canonical.String(),
		signed.String(),
		"UNSIGNED-PAYLOAD",
	}, "\n")
	digest := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" +
		day + "/" + region + "/" + service + "/aws4_request\n" +
		hex.EncodeToString(digest[:])
	signingKey := hmacSHA(hmacSHA(hmacSHA(hmacSHA(
		[]byte("AWS4"+secretKey), day), region), service), "aws4_request")
	signature := hex.EncodeToString(hmacSHA(signingKey, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s/%s/%s/aws4_request, SignedHeaders=%s, Signature=%s",
		accessKey, day, region, service, signed.String(), signature))
}

// copyAuditChain builds the chain NewServer builds over two buckets that one
// restricted key may read and write.
func copyAuditChain(t *testing.T) http.Handler {
	t.Helper()
	ctx := context.Background()
	root := "/" + sanitizeTestName(t.Name())

	for _, suffix := range []string{"src", "dst"} {
		localRoot, err := os.MkdirTemp("", "openlist-s3-copy-*")
		if err != nil {
			t.Fatalf("mkdir local root: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(localRoot) })
		addition, err := json.Marshal(struct {
			RootFolderPath string `json:"root_folder_path"`
			Thumbnail      bool   `json:"thumbnail"`
		}{RootFolderPath: localRoot, Thumbnail: false})
		if err != nil {
			t.Fatalf("marshal storage addition: %v", err)
		}
		if _, err := op.CreateStorage(ctx, model.Storage{
			Driver:    "Local",
			MountPath: root + "-" + suffix,
			Addition:  string(addition),
		}); err != nil {
			t.Fatalf("create storage %s: %+v", suffix, err)
		}
	}

	setTestSetting(conf.S3Buckets, `[{"name":"src-bucket","path":"`+root+`-src"},`+
		`{"name":"dst-bucket","path":"`+root+`-dst"}]`)
	setTestSetting(conf.S3BucketAuth, copyAuditRule)
	setTestSetting(conf.S3AccessKeyId, "")
	setTestSetting(conf.S3SecretAccessKey, "")
	setTestSetting(conf.S3BucketCors, "")
	t.Cleanup(func() {
		setTestSetting(conf.S3Buckets, "[]")
		setTestSetting(conf.S3BucketAuth, "[]")
		setTestSetting(conf.S3BucketCors, "")
	})

	serverCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	handler, err := NewServer(serverCtx)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return handler
}

func signedPut(t *testing.T, handler http.Handler, target, body string, extra map[string]string, signedNames []string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, target, strings.NewReader(body))
	req.Header.Set("Content-Length", fmt.Sprint(len(body)))
	for name, value := range extra {
		req.Header.Set(name, value)
	}
	signV4Header(t, req, "leaky-key", "leaky-secret", signedNames)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHeaderSignedCopySourceMustBeSigned(t *testing.T) {
	handler := copyAuditChain(t)

	base := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if rec := signedPut(t, handler, "/src-bucket/secret.txt", "top secret", nil, base); rec.Code != http.StatusOK {
		t.Fatalf("seeding the source object failed: %d %s", rec.Code, rec.Body.String())
	}

	// The interesting request: the signature covers host and the amz framing
	// headers only, and the copy source is attached afterwards, exactly the way
	// an intermediary holding a signed request would add it.
	rec := signedPut(t, handler, "/dst-bucket/stolen.txt", "", map[string]string{
		"X-Amz-Copy-Source": "/src-bucket/secret.txt",
	}, base)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("an unsigned X-Amz-Copy-Source was honoured: code=%d body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "AccessDenied") {
		t.Fatalf("refusal does not name AccessDenied: %s", rec.Body.String())
	}

	// Nothing may have been copied.
	read := httptest.NewRequest(http.MethodGet, "/dst-bucket/stolen.txt", nil)
	signV4Header(t, read, "leaky-key", "leaky-secret", base)
	readRec := httptest.NewRecorder()
	handler.ServeHTTP(readRec, read)
	if readRec.Code == http.StatusOK {
		t.Fatalf("the destination key exists after a refused copy: %d %q", readRec.Code, readRec.Body.String())
	}

	// The same copy with the header inside the signature keeps working, so the
	// check cannot be satisfied by refusing every copy.
	rec = signedPut(t, handler, "/dst-bucket/copied.txt", "", map[string]string{
		"X-Amz-Copy-Source": "/src-bucket/secret.txt",
	}, append(append([]string(nil), base...), "x-amz-copy-source"))
	if rec.Code != http.StatusOK {
		t.Fatalf("a signed copy source was refused: code=%d body=%s", rec.Code, rec.Body.String())
	}

	read = httptest.NewRequest(http.MethodGet, "/dst-bucket/copied.txt", nil)
	signV4Header(t, read, "leaky-key", "leaky-secret", base)
	readRec = httptest.NewRecorder()
	handler.ServeHTTP(readRec, read)
	if readRec.Code != http.StatusOK || readRec.Body.String() != "top secret" {
		t.Fatalf("the signed copy did not carry the bytes: %d %q", readRec.Code, readRec.Body.String())
	}
}
