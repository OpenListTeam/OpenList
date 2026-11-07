package s3

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/OpenListTeam/gofakes3/signature"
)

// errChunkedOverrun is returned when an aws-chunked body declares more bytes in
// its frames than x-amz-decoded-content-length announced.
var errChunkedOverrun = errors.New("aws-chunked body carries more bytes than x-amz-decoded-content-length declared")
var errChunkSignature = errors.New("aws-chunked chunk signature does not match")

// chunkFrameWatcher counts the bytes an aws-chunked body declares in its frame
// headers and refuses a body that carries more of them than it announced.
//
// gofakes3 sizes its frame decoder with io.LimitReader(declaredLength)
// (gofakes3/gofakes3.go:776, :1081), so the surplus is discarded before the
// storage layer sees it: putStream's "written != want" check can never fire for
// this direction, and the object is stored truncated with 200 OK. Nothing on
// the client side notices either, because the ETag is computed from what
// actually arrived. The frames are parsed here — above that limit — so the
// overrun is caught while the body streams and nothing is committed.
//
// The watcher is a pass-through: every byte it reads is handed on unchanged,
// including the framing itself, because gofakes3 decodes the frames again.
type chunkFrameWatcher struct {
	inner    io.ReadCloser
	reader   *bufio.Reader
	declared int64
	seen     int64
	// remaining is how many body bytes of the current frame have not been
	// passed on yet.
	remaining int64
	// inBody reports whether the next bytes are frame payload rather than a
	// frame header.
	inBody bool
	// pending holds header bytes that were read ahead but not yet handed to the
	// caller (a header can be longer than the caller's buffer).
	pending []byte
	// inTrailer reports that the terminating zero-length chunk was reached and
	// the trailer section is being read: its lines are passed through untouched
	// while the checksum they may carry is remembered.
	inTrailer bool
	// finished reports that the trailer's terminating empty line was reached:
	// everything after it is passed through untouched.
	finished bool
	// algorithm is the x-amz-checksum-* algorithm the client announced in
	// x-amz-trailer, and hasher accumulates the body it declares. Both are empty
	// when the request carries no trailer checksum this gateway can verify.
	algorithm string
	hasher    hash.Hash
	// trailerDeclared is the value the trailer gave for algorithm.
	trailerDeclared string
	err             error
	// signingKey, scope and timestamp authenticate each frame in the
	// SigV4 streaming chain. previous starts as the seed request signature.
	signingKey     []byte
	scope          string
	timestamp      string
	previous       string
	verify         bool
	frameHasher    hash.Hash
	frameSignature string
}

func (w *chunkFrameWatcher) authenticate(signatureHex, payloadHash string) error {
	if !w.verify {
		return nil
	}
	provided, err := hex.DecodeString(signatureHex)
	if err != nil || len(provided) != sha256.Size {
		return errChunkSignature
	}
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256-PAYLOAD", w.timestamp, w.scope, w.previous,
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", payloadHash,
	}, "\n")
	mac := hmac.New(sha256.New, w.signingKey)
	_, _ = mac.Write([]byte(stringToSign))
	if subtle.ConstantTimeCompare(mac.Sum(nil), provided) != 1 {
		return errChunkSignature
	}
	w.previous = signatureHex
	return nil
}

// enableSignatureVerification binds the frame chain to the already verified
// seed request. V4SignVerifyWithLookup validates the seed signature, while the
// credential scope supplies the same signing key used by the client.
func (w *chunkFrameWatcher) enableSignatureVerification(r *http.Request, keys *keyRing) error {
	if keys == nil || keys.empty() {
		return nil
	}
	if err := signature.V4SignVerifyWithLookup(r, keys.secret); err != signature.ErrNone {
		return errChunkSignature
	}
	authorization := r.Header.Get("Authorization")
	credential, ok := authorizationField(authorization, "Credential")
	seed, seedOK := authorizationField(authorization, "Signature")
	parts := strings.Split(credential, "/")
	if !ok || !seedOK || len(parts) < 5 {
		return errChunkSignature
	}
	accessKey := strings.Join(parts[:len(parts)-4], "/")
	secret, ok := keys.secret(accessKey)
	if !ok {
		return errChunkSignature
	}
	date, region, service := parts[len(parts)-4], parts[len(parts)-3], parts[len(parts)-2]
	if service != "s3" || parts[len(parts)-1] != "aws4_request" {
		return errChunkSignature
	}
	w.timestamp = r.Header.Get("X-Amz-Date")
	if w.timestamp == "" {
		return errChunkSignature
	}
	w.scope = strings.Join(parts[len(parts)-4:], "/")
	w.previous = seed
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	w.signingKey = hmacSHA256(key, "aws4_request")
	w.verify = true
	return nil
}

func authorizationField(authorization, name string) (string, bool) {
	for _, field := range strings.Split(authorization, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(field), "=")
		if index := strings.LastIndexByte(key, ' '); index >= 0 {
			key = key[index+1:]
		}
		if ok && key == name && value != "" {
			return value, true
		}
	}
	return "", false
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(value))
	return mac.Sum(nil)
}

func newChunkFrameWatcher(inner io.ReadCloser, declared int64, trailer string) *chunkFrameWatcher {
	watcher := &chunkFrameWatcher{inner: inner, reader: bufio.NewReader(inner), declared: declared}
	// A trailer checksum is the one integrity value a streaming upload can carry:
	// the client names it in x-amz-trailer and sends its value after the last
	// frame, so the body has to be hashed while it streams.
	for _, name := range strings.Split(trailer, ",") {
		candidate := strings.ToLower(strings.TrimSpace(name))
		algorithm := strings.TrimPrefix(candidate, checksumHeaderPrefix)
		if algorithm == candidate {
			continue
		}
		if hasher := newChecksumHash(algorithm); hasher != nil {
			watcher.algorithm = algorithm
			watcher.hasher = hasher
			break
		}
	}
	return watcher
}

func (w *chunkFrameWatcher) Read(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	total := 0
	for total < len(p) {
		if w.finished {
			// Read through the buffered reader: it may already hold bytes that
			// follow the final chunk (the trailer), and reading from the inner
			// reader would drop them.
			n, err := w.reader.Read(p[total:])
			total += n
			if total > 0 {
				return total, nil
			}
			return 0, err
		}
		if len(w.pending) > 0 {
			n := copy(p[total:], w.pending)
			w.pending = w.pending[n:]
			total += n
			continue
		}
		if w.inBody {
			want := int64(len(p) - total)
			if want > w.remaining {
				want = w.remaining
			}
			n, err := io.ReadFull(w.reader, p[total:total+int(want)])
			if n > 0 && w.frameHasher != nil {
				_, _ = w.frameHasher.Write(p[total : total+n])
			}
			if n > 0 && w.hasher != nil {
				// The frame payload is what the client's checksum covers.
				_, _ = w.hasher.Write(p[total : total+n])
			}
			total += n
			w.remaining -= int64(n)
			if w.remaining == 0 {
				w.inBody = false
				if err := w.authenticate(w.frameSignature, hex.EncodeToString(w.frameHasher.Sum(nil))); err != nil {
					w.err = err
					if total > 0 {
						return total, nil
					}
					return 0, err
				}
			}
			if err != nil {
				// The body ended in the middle of a frame: gofakes3's own
				// decoder reports that, so the error is left to it.
				if total > 0 {
					return total, nil
				}
				return 0, err
			}
			continue
		}
		line, err := w.reader.ReadString('\n')
		if err != nil && line == "" {
			if total > 0 {
				return total, nil
			}
			return 0, err
		}
		w.pending = []byte(line)
		if err := w.consumeLine(line); err != nil {
			w.err = err
			if total > 0 {
				return total, nil
			}
			return 0, err
		}
	}
	return total, nil
}

// consumeLine parses one line of the framed body: a frame header while the
// frames are being read, a trailer header once the terminating chunk arrived.
func (w *chunkFrameWatcher) consumeLine(line string) error {
	if !w.inTrailer {
		return w.consumeHeader(line)
	}
	trimmed := strings.TrimRight(line, "\r\n")
	if trimmed == "" {
		// The empty line that ends the trailer: nothing follows the frames.
		w.finished = true
		return nil
	}
	w.consumeTrailerLine(trimmed)
	return nil
}

// consumeTrailerLine records the checksum a trailer header declares.
func (w *chunkFrameWatcher) consumeTrailerLine(line string) {
	if w.algorithm == "" {
		return
	}
	key, value, found := strings.Cut(line, ":")
	if !found {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(key), checksumHeaderPrefix+w.algorithm) {
		return
	}
	w.trailerDeclared = strings.TrimSpace(value)
}

// consumeHeader parses one frame header line and records what it announces.
func (w *chunkFrameWatcher) consumeHeader(line string) error {
	trimmed := strings.TrimRight(line, "\r\n")
	if trimmed == "" {
		// The CRLF that closes the previous frame's payload.
		return nil
	}
	raw := trimmed
	signatureHex := ""
	if index := strings.IndexByte(raw, ';'); index >= 0 {
		for _, extension := range strings.Split(raw[index+1:], ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(extension), "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "chunk-signature") {
				signatureHex = strings.TrimSpace(value)
			}
		}
		raw = raw[:index]
	}
	size, err := strconv.ParseInt(strings.TrimSpace(raw), 16, 64)
	if err != nil || size < 0 {
		// Not a frame header this watcher understands (a trailer, or something
		// gofakes3 will reject on its own): stop watching and pass the rest
		// through untouched.
		w.finished = true
		return nil
	}
	if size == 0 {
		if err := w.authenticate(signatureHex, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"); err != nil {
			return err
		}
		w.inTrailer = true
		return nil
	}
	w.seen += size
	if w.declared >= 0 && w.seen > w.declared {
		return errChunkedOverrun
	}
	w.frameSignature = signatureHex
	w.frameHasher = sha256.New()
	w.remaining = size
	w.inBody = true
	return nil
}

func (w *chunkFrameWatcher) Close() error {
	return w.inner.Close()
}

// chunkWatcherKey carries the frame watcher to the storage layer, which is the
// only place that knows the decoder reached the end of the announced length.
type chunkWatcherKey struct{}

func chunkWatcherFromContext(ctx context.Context) *chunkFrameWatcher {
	if ctx == nil {
		return nil
	}
	watcher, _ := ctx.Value(chunkWatcherKey{}).(*chunkFrameWatcher)
	return watcher
}

// overrunAfterDecodedBody reports whether the framed body carries more frame
// data than the decoder was allowed to consume.
//
// The early detection inside Read only fires when the surplus happens to be
// read ahead; a decoder that stops exactly on a frame boundary leaves the next
// frame header unread, so the surplus has to be looked for once the write has
// finished. Any non-empty frame after the announced length is surplus.
func (w *chunkFrameWatcher) overrunAfterDecodedBody() bool {
	if w.err != nil {
		return true
	}
	if w.finished {
		return w.seen > w.declared
	}
	// Bounded like net/http's own drain (maxPostHandlerReadBytes): the surplus
	// of a normal request is a few bytes, and a body that never ends is a
	// broken client, not something to wait for.
	budget := int64(1 << 20)
	for budget > 0 {
		if w.inBody {
			n := w.remaining
			if n > budget {
				n = budget
			}
			copied, err := io.CopyN(io.Discard, w.reader, n)
			budget -= copied
			w.remaining -= copied
			if w.remaining == 0 {
				w.inBody = false
				if err := w.authenticate(w.frameSignature, hex.EncodeToString(w.frameHasher.Sum(nil))); err != nil {
					w.err = err
					return true
				}
			}
			if err != nil {
				return false
			}
			continue
		}
		line, err := w.reader.ReadString('\n')
		budget -= int64(len(line))
		if line == "" && err != nil {
			return false
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if w.inTrailer {
			if trimmed == "" {
				w.finished = true
				return w.seen > w.declared
			}
			w.consumeTrailerLine(trimmed)
			continue
		}
		if trimmed == "" {
			// The CRLF that closes the previous frame's payload.
			if err != nil {
				return false
			}
			continue
		}
		if cerr := w.consumeHeader(line); cerr != nil {
			w.err = cerr
			return true
		}
		if w.inTrailer {
			// The terminating chunk: its trailer follows.
			continue
		}
		if w.finished {
			return false
		}
		// The decoder already consumed everything it was allowed to; another
		// non-empty frame is surplus.
		return true
	}
	return false
}

// verifiedTrailerChecksum returns the checksum a framed body declared in its
// trailer once the trailer has been read and the value matched the body.
//
// A client that asks the gateway to verify a trailer checksum expects the value
// to be part of the object afterwards: S3 answers it on the PUT and serves it on
// later GET/HEAD calls. Verifying it and dropping it made the trailer a
// one-shot check the client could not confirm.
func (w *chunkFrameWatcher) verifiedTrailerChecksum() (string, string, bool) {
	if w == nil || w.hasher == nil || w.trailerDeclared == "" || !w.finished {
		return "", "", false
	}
	if w.trailerChecksumMismatch() {
		return "", "", false
	}
	return w.algorithm, w.trailerDeclared, true
}

// trailerChecksumMismatch reports whether the trailer declared a checksum this
// gateway could verify and the body does not match it. It is only meaningful
// once the trailer has been read (overrunAfterDecodedBody drains up to it).
func (w *chunkFrameWatcher) trailerChecksumMismatch() bool {
	if w.hasher == nil || w.trailerDeclared == "" {
		return false
	}
	return !checksumMatches(w.algorithm, w.hasher.Sum(nil), w.trailerDeclared)
}

// resignSeedAuthorization re-mints the SigV4 seed signature of a streaming
// request whose X-Amz-Content-Sha256 the gateway had to rewrite after the
// client's own seed was verified. gofakes3 verifies the Authorization header
// once more inside its own auth middleware, against the rewritten header, so
// the gateway signs the rewritten request itself; the client was already
// authenticated against the original bytes.
func resignSeedAuthorization(r *http.Request, secret string) {
	authorization := r.Header.Get("Authorization")
	credential, ok := authorizationField(authorization, "Credential")
	signedList, okList := authorizationField(authorization, "SignedHeaders")
	if !ok || !okList {
		return
	}
	parts := strings.Split(credential, "/")
	if len(parts) < 5 {
		return
	}
	date, region, service := parts[len(parts)-4], parts[len(parts)-3], parts[len(parts)-2]

	names := strings.Split(signedList, ";")
	sort.Strings(names)
	var canonical strings.Builder
	for _, name := range names {
		value := ""
		if name == "host" {
			value = r.Host
		} else {
			value = strings.Join(strings.Fields(r.Header.Get(name)), " ")
		}
		canonical.WriteString(name)
		canonical.WriteString(":")
		canonical.WriteString(value)
		canonical.WriteString("\n")
	}
	// rebuild signed list in the same sorted order
	sorted := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		r.Method,
		encodeS3Path(r.URL.Path),
		r.URL.RawQuery,
		canonical.String(),
		sorted,
		r.Header.Get("X-Amz-Content-Sha256"),
	}, "\n")
	digest := sha256.Sum256([]byte(canonicalRequest))
	scope := strings.Join(parts[len(parts)-4:], "/")
	timestamp := r.Header.Get("X-Amz-Date")
	stringToSign := "AWS4-HMAC-SHA256\n" + timestamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	key := hmacSHA256([]byte("AWS4"+secret), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "aws4_request")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(stringToSign))
	newAuthorization := "AWS4-HMAC-SHA256 Credential=" + credential +
		", SignedHeaders=" + sorted + ", Signature=" + hex.EncodeToString(mac.Sum(nil))
	r.Header.Set("Authorization", newAuthorization)
}

// encodeS3Path mirrors the signature library's path encoding for the common
// case of object names that need no escaping.
func encodeS3Path(path string) string {
	return (&url.URL{Path: path}).EscapedPath()
}
