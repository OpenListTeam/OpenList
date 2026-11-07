package s3

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// streamingChain is what a SigV4 streaming upload computes: the seed request
// signature authenticates the headers, and each frame's signature chains from
// the previous one through the signing key derived from the secret.
type streamingChain struct {
	seed       string
	timestamp  string
	scope      string
	signingKey []byte
	previous   string
	emptyHash  string
}

const (
	streamVariantSigned        = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
	streamVariantSignedTrailer = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER"
)

func newStreamingChain(t *testing.T, secretKey string, headers map[string]string) streamingChain {
	t.Helper()
	now := time.Now().UTC()
	stamp := now.Format("20060102T150405Z")
	day := now.Format("20060102")
	const region, service = "us-east-1", "s3"
	scope := day + "/" + region + "/" + service + "/aws4_request"

	key := hmacSHA256([]byte("AWS4"+secretKey), day)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")

	sum := sha256.Sum256([]byte(""))
	headers["X-Amz-Date"] = stamp
	chain := streamingChain{
		timestamp:  stamp,
		scope:      scope,
		signingKey: key,
		emptyHash:  hex.EncodeToString(sum[:]),
	}

	signed := []string{"content-encoding", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-decoded-content-length"}
	if _, ok := headers["X-Amz-Trailer"]; ok {
		signed = append(signed, "x-amz-trailer")
	}
	var canonical, signedList strings.Builder
	for i, name := range signed {
		value := headers[http.CanonicalHeaderKey(name)]
		if name == "host" {
			value = "localhost"
		}
		canonical.WriteString(name + ":" + value + "\n")
		if i > 0 {
			signedList.WriteString(";")
		}
		signedList.WriteString(name)
	}
	payload := headers["X-Amz-Content-Sha256"]
	canonicalRequest := strings.Join([]string{
		http.MethodPut, "/st/frame.bin", "", canonical.String(), signedList.String(), payload,
	}, "\n")
	digest := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(stringToSign))
	chain.seed = hex.EncodeToString(mac.Sum(nil))
	chain.previous = chain.seed
	return chain
}

// frameSignature signs one frame of the chain and advances to the next.
func (c *streamingChain) frameSignature(payload []byte) string {
	sum := sha256.Sum256(payload)
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256-PAYLOAD", c.timestamp, c.scope, c.previous, c.emptyHash, hex.EncodeToString(sum[:]),
	}, "\n")
	mac := hmac.New(sha256.New, c.signingKey)
	_, _ = mac.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac.Sum(nil))
	c.previous = signature
	return signature
}

func (c *streamingChain) authorization(accessKey string, headers map[string]string) string {
	signed := []string{"content-encoding", "host", "x-amz-content-sha256", "x-amz-date", "x-amz-decoded-content-length"}
	if _, ok := headers["X-Amz-Trailer"]; ok {
		signed = append(signed, "x-amz-trailer")
	}
	for i := range signed {
		signed[i] = strings.ToLower(signed[i])
	}
	return "AWS4-HMAC-SHA256 Credential=" + accessKey + "/" + c.scope +
		", SignedHeaders=" + strings.Join(signed, ";") + ", Signature=" + c.seed
}

// signedStreamingFrames encodes payload as aws-chunked frames with real chain
// signatures. corrupt names the chunk whose signature is deliberately wrong.
func signedStreamingFrames(chain *streamingChain, payload string, chunkSize int, corrupt int) string {
	var builder strings.Builder
	rest := []byte(payload)
	sent := 0
	for len(rest) > 0 {
		size := chunkSize
		if size > len(rest) {
			size = len(rest)
		}
		chunk := rest[:size]
		signature := chain.frameSignature(chunk)
		if sent == corrupt {
			signature = strings.Repeat("0", 64)
		}
		fmt.Fprintf(&builder, "%x;chunk-signature=%s\r\n", size, signature)
		builder.Write(chunk)
		builder.WriteString("\r\n")
		rest = rest[size:]
		sent++
	}
	signature := chain.frameSignature(nil)
	if corrupt == sent {
		signature = strings.Repeat("0", 64)
	}
	fmt.Fprintf(&builder, "0;chunk-signature=%s\r\n", signature)
	return builder.String()
}

// signedTrailerFrames is the -TRAILER variant: signed frames followed by a
// trailer section carrying a checksum header.
func signedTrailerFrames(chain *streamingChain, payload string, chunkSize int, corrupt int) string {
	// The storage layer verifies the trailer checksum, so it has to be the real
	// CRC32 of the payload or the valid upload is refused with BadDigest.
	sum := crc32.ChecksumIEEE([]byte(payload))
	value := base64.StdEncoding.EncodeToString([]byte{byte(sum >> 24), byte(sum >> 16), byte(sum >> 8), byte(sum)})
	body := signedStreamingFrames(chain, payload, chunkSize, corrupt)
	return body + "x-amz-checksum-crc32:" + value + "\r\n\r\n"
}

// streamingTestChain is a signed bucket behind the full NewServer chain.
func streamingTestChain(t *testing.T) (http.Handler, string) {
	t.Helper()
	setupMultipartBackend(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	root, err := os.MkdirTemp("", "openlist-s3-stream-*")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	mount := "/stream-" + sanitizeTestName(t.Name())

	addition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: root})
	if err != nil {
		t.Fatalf("marshal addition: %v", err)
	}
	if _, err := op.CreateStorage(ctx, model.Storage{
		Driver:    "Local",
		MountPath: mount,
		Addition:  string(addition),
	}); err != nil {
		t.Fatalf("create storage: %v", err)
	}
	setTestSetting(conf.S3Buckets, `[{"name":"st","path":"`+mount+`"}]`)
	setTestSetting(conf.S3BucketAuth, `[{"access_key":"st-key","secret_key":"st-secret","buckets":["st"],"readonly":false}]`)
	t.Cleanup(func() {
		setTestSetting(conf.S3Buckets, "[]")
		setTestSetting(conf.S3BucketAuth, "[]")
	})

	handler, err := NewServer(ctx)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return handler, root
}

// signedStreamingPut sends one streaming PUT over a raw connection, because the
// body is pre-framed bytes that must reach the server verbatim. It signs the
// seed from the real headers and frames the body with the same chain, the way
// an SDK does; corrupt names the chunk whose signature to falsify, -1 for none.
func signedStreamingPut(t *testing.T, handler http.Handler, variant, body, trailer string, chunkSize, corrupt int) (int, string) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	reader := bufio.NewReader(conn)

	headers := map[string]string{
		"Content-Encoding":             "aws-chunked",
		"X-Amz-Content-Sha256":         variant,
		"X-Amz-Decoded-Content-Length": fmt.Sprint(len(body)),
	}
	if trailer != "" {
		headers["X-Amz-Trailer"] = trailer
	}
	chain := newStreamingChain(t, "st-secret", headers)
	var framed string
	if variant == streamVariantSignedTrailer {
		framed = signedTrailerFrames(&chain, body, chunkSize, corrupt)
	} else {
		framed = signedStreamingFrames(&chain, body, chunkSize, corrupt)
	}
	request := "PUT /st/frame.bin HTTP/1.1\r\nHost: localhost\r\nContent-Encoding: aws-chunked\r\n" +
		"X-Amz-Content-Sha256: " + variant + "\r\n" +
		"X-Amz-Date: " + chain.timestamp + "\r\n" +
		"X-Amz-Decoded-Content-Length: " + fmt.Sprint(len(body)) + "\r\n"
	if trailer != "" {
		request += "X-Amz-Trailer: " + trailer + "\r\n"
	}
	request += "Content-Length: " + fmt.Sprint(len(framed)) + "\r\n" +
		"Authorization: " + chain.authorization("st-key", headers) + "\r\n\r\n" + framed
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	responseBody := make([]byte, 4096)
	n, _ := resp.Body.Read(responseBody)
	return resp.StatusCode, string(responseBody[:n])
}

// A streaming upload that uses the signed -TRAILER framing signs its frames
// exactly like the plain signed variant. Normalising it to the unsigned form
// without enabling frame verification let any tampered frame through with
// 200 OK: the seed signature covers the headers only, so nothing else
// authenticated the bytes.
func TestSignedTrailerStreamingFramesAreVerified(t *testing.T) {
	handler, root := streamingTestChain(t)

	payload := "0123456789abcdefghij"
	if code, body := signedStreamingPut(t, handler, streamVariantSignedTrailer, payload, "X-Amz-Checksum-Crc32", 8, -1); code != http.StatusOK {
		t.Fatalf("valid signed-trailer upload: code=%d body=%s, want 200", code, body)
	}
	got, err := os.ReadFile(filepath.Join(root, "frame.bin"))
	if err != nil || string(got) != payload {
		t.Fatalf("stored body %q err=%v", got, err)
	}

	if code, body := signedStreamingPut(t, handler, streamVariantSignedTrailer, payload, "X-Amz-Checksum-Crc32", 8, 1); code != http.StatusForbidden && code != http.StatusBadRequest {
		t.Fatalf("tampered signed-trailer upload: code=%d body=%s, want 400/403", code, body)
	}
	if got, err := os.ReadFile(filepath.Join(root, "frame.bin")); err != nil || string(got) != payload {
		t.Fatalf("the tampered upload changed the object: %q err=%v", got, err)
	}
}

// The plain signed variant keeps its round-35 behaviour.
func TestSignedStreamingFramesAreStillVerified(t *testing.T) {
	handler, root := streamingTestChain(t)

	payload := "0123456789"
	if code, body := signedStreamingPut(t, handler, streamVariantSigned, payload, "", 4, -1); code != http.StatusOK {
		t.Fatalf("valid signed upload: code=%d body=%s, want 200", code, body)
	}
	if code, body := signedStreamingPut(t, handler, streamVariantSigned, payload, "", 4, 0); code != http.StatusForbidden && code != http.StatusBadRequest {
		t.Fatalf("tampered signed upload: code=%d body=%s, want 400/403", code, body)
	}
	if got, err := os.ReadFile(filepath.Join(root, "frame.bin")); err != nil || string(got) != payload {
		t.Fatalf("the tampered upload changed the object: %q err=%v", got, err)
	}
}
