package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxPresignedLifetimeSeconds is AWS's limit for a presigned URL (7 days).
const maxPresignedLifetimeSeconds = 7 * 24 * 60 * 60

// maxPresignedClockSkew is how far in the future a presigned URL may be dated.
const maxPresignedClockSkew = 15 * time.Minute

// fixV2PresignedSignature repairs gofakes3's double url.QueryUnescape bug on
// Signature Version 2 presigned query strings. gofakes3 calls
// req.URL.Query().Get("Signature") (which already decodes the query component)
// and then calls url.QueryUnescape on the result, turning '+' in the base64
// signature into a space. Escaping any '+' as '%252B' ensures the second unescape
// produces the original '+' instead of space.
func fixV2PresignedSignature(r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		return
	}
	rawQuery := r.URL.RawQuery
	if !strings.Contains(rawQuery, "AWSAccessKeyId=") || !strings.Contains(rawQuery, "Signature=") {
		return
	}
	sig := r.URL.Query().Get("Signature")
	if sig == "" || !strings.Contains(sig, "+") {
		return
	}
	escapedSig := strings.ReplaceAll(url.QueryEscape(sig), "%", "%25")
	parts := strings.Split(rawQuery, "&")
	for i, part := range parts {
		if strings.HasPrefix(part, "Signature=") {
			parts[i] = "Signature=" + escapedSig
			break
		}
	}
	r.URL.RawQuery = strings.Join(parts, "&")
}

// presignedLifetimeGuard bounds presigned URLs. gofakes3 rejects an expired one
// and does nothing else: X-Amz-Expires has no upper limit, and the date in the
// query is never compared against the future. A signer could therefore mint a
// link valid for decades, and a badly skewed clock could mint one dated a year
// ahead and then keep using it, so both ends are checked here.
func presignedLifetimeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixV2PresignedSignature(r)
		if rejectSkewedRequest(w, r) {
			return
		}
		// The checks below key off the query parameters, not off "is this a
		// presigned URL". gofakes3 reads X-Amz-Expires on every request, so a
		// header-signed request that merely carries ?X-Amz-Expires=... was both
		// accepted and exempt from the ceiling: a holder of any working key could
		// mint a (method, key, date, signature) tuple that never expires.
		query := r.URL.Query()
		bucket := bucketOnlyName(r.URL.Path)
		_, hasV4Expires := query["X-Amz-Expires"]
		_, hasV2Expires := query["Expires"]
		rawDate := strings.TrimSpace(query.Get("X-Amz-Date"))
		if rawDate == "" {
			// A V4 request signs the timestamp in the header, and gofakes3 never
			// compares it against the clock: a signature made with a date a year
			// ahead stays usable for a year.
			rawDate = strings.TrimSpace(r.Header.Get("X-Amz-Date"))
		}
		if !hasV4Expires && !hasV2Expires && rawDate == "" {
			next.ServeHTTP(w, r)
			return
		}

		if hasV2Expires {
			// Signature Version 2 spells the expiry as an absolute unix time and
			// gofakes3 only checks whether it has passed, so a V2 link minted for
			// the year 2100 authenticates forever. The same 7-day ceiling AWS
			// applies to V4 has to be enforced here.
			raw := strings.TrimSpace(query.Get("Expires"))
			expires, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				writeS3Error(w, r, http.StatusBadRequest, "AuthorizationQueryParametersError", bucket,
					"Expires must be a unix timestamp in seconds.")
				return
			}
			lifetime := time.Until(time.Unix(expires, 0))
			if lifetime > maxPresignedLifetimeSeconds*time.Second {
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket,
					fmt.Sprintf("Expires must be at most %d seconds (7 days) from now.", maxPresignedLifetimeSeconds))
				return
			}
			if time.Now().After(time.Unix(expires, 0)) {
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket, "Request has expired.")
				return
			}
		}

		if raw := strings.TrimSpace(query.Get("X-Amz-Expires")); hasV4Expires {
			expires, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || expires < 0 {
				writeS3Error(w, r, http.StatusBadRequest, "AuthorizationQueryParametersError", bucket,
					"X-Amz-Expires must be a non-negative integer number of seconds.")
				return
			}
			if expires > maxPresignedLifetimeSeconds {
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket,
					fmt.Sprintf("X-Amz-Expires must be at most %d seconds (7 days).", maxPresignedLifetimeSeconds))
				return
			}
		}

		if rawDate != "" {
			issued, err := time.Parse("20060102T150405Z", rawDate)
			if err == nil && issued.After(time.Now().Add(maxPresignedClockSkew)) {
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket,
					"X-Amz-Date is too far in the future.")
				return
			}
			// An expired link is an authorization failure, and S3 says so with
			// 403 AccessDenied "Request has expired". gofakes3 answers the same
			// situation with 400 (signature-errors.go:127 hardcodes it), which
			// made the gateway report the same class of refusal with two
			// different statuses: >7 days 403, already expired 400.
			// No grace period on the expiry itself: S3 stops honouring a
			// presigned URL the second X-Amz-Date+X-Amz-Expires has passed, while
			// the fifteen minute allowance applies to clock skew between signer
			// and server, not to the link's own lifetime. Leaving the grace in
			// sent every link that expired less than fifteen minutes ago to
			// gofakes3, which answers 400 with "the difference between the
			// request time and the server's time exceeds the maximum allowed" --
			// a message about clocks for what is really an expired link.
			if expires, ok := presignedExpiry(rawDate, query); ok && time.Now().After(expires) {
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket, "Request has expired.")
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// presignedExpiry returns the instant a presigned URL stops being valid: the
// request date plus X-Amz-Expires (V4) or the absolute Expires value (V2).
func presignedExpiry(rawDate string, query url.Values) (time.Time, bool) {
	if raw := strings.TrimSpace(query.Get("X-Amz-Expires")); raw != "" {
		issued, err := time.Parse("20060102T150405Z", rawDate)
		if err != nil {
			return time.Time{}, false
		}
		seconds, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || seconds < 0 {
			return time.Time{}, false
		}
		return issued.Add(time.Duration(seconds) * time.Second), true
	}
	if raw := strings.TrimSpace(query.Get("Expires")); raw != "" {
		seconds, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.Unix(seconds, 0), true
	}
	return time.Time{}, false
}

// maxRequestClockSkew is the window S3 allows between the timestamp a request
// was signed with and the server's clock.
const maxRequestClockSkew = 15 * time.Minute

// requestTimeGuard answers a header-signed request whose timestamp is outside
// the fifteen minute window with S3's own code.
//
// gofakes3 applies the same window but reports it as 400 AccessDenied for a date
// in the past and 403 AccessDenied for a date in the future, so a client cannot
// tell a skewed clock from a signature failure. AWS answers 403
// RequestTimeTooSkewed for both, and SDKs key their "your clock is wrong,
// resynchronise and retry" advice off that code.
func rejectSkewedRequest(w http.ResponseWriter, r *http.Request) bool {
	// Presigned requests date themselves in the query and are bounded by
	// X-Amz-Expires instead (presignedLifetimeGuard): a link signed an hour
	// ago with a two hour lifetime is valid, so the skew window must not be
	// applied to it.
	query := r.URL.Query()
	if query.Get("X-Amz-Signature") != "" || query.Get("Signature") != "" {
		return false
	}
	if strings.TrimSpace(r.Header.Get("Authorization")) == "" {
		return false
	}
	signedAt, ok := requestSignedAt(r)
	if !ok {
		return false
	}
	if skew := time.Since(signedAt); skew > maxRequestClockSkew || skew < -maxRequestClockSkew {
		writeS3Error(w, r, http.StatusForbidden, "RequestTimeTooSkewed", bucketOnlyName(r.URL.Path),
			"The difference between the request time and the current time is too large.")
		return true
	}
	return false
}

// requestSignedAt returns the timestamp a signed request declares: SigV4 puts it
// in x-amz-date, SigV2 in Date (or x-amz-date).
func requestSignedAt(r *http.Request) (time.Time, bool) {
	if raw := strings.TrimSpace(r.Header.Get("X-Amz-Date")); raw != "" {
		if signedAt, err := time.Parse("20060102T150405Z", raw); err == nil {
			return signedAt, true
		}
	}
	if raw := strings.TrimSpace(r.Header.Get("Date")); raw != "" {
		if signedAt, err := http.ParseTime(raw); err == nil {
			return signedAt, true
		}
	}
	return time.Time{}, false
}

// payloadHashKey carries the declared payload hash into the storage layer.
type payloadHashKey struct{}

// withoutPayloadExpectation drops the declared payload hash and length for a
// storage write that does not carry the request body. CompleteMultipartUpload
// assembles the object from the uploaded parts while its body is the XML part
// list, so verifying the assembled bytes against that body's hash rejected
// every completion sent by a client that signs its payloads (boto3 always does).
func withoutPayloadExpectation(ctx context.Context) context.Context {
	return context.WithValue(ctx, payloadHashKey{}, "")
}

// declaredPayloadHash returns the payload hash a SigV4 request declared, or ""
// when the request declared none (or declared UNSIGNED-PAYLOAD).
func declaredPayloadHash(ctx context.Context) string {
	if v, ok := ctx.Value(payloadHashKey{}).(string); ok {
		return v
	}
	return ""
}

// errPayloadHashMismatch is returned by hashingBody once the body has been read
// to the end and its SHA-256 does not match what the request declared.
var errPayloadHashMismatch = errors.New("x-amz-content-sha256 mismatch")

// contentSha256Guard checks the payload hash a SigV4 request declares. gofakes3
// folds X-Amz-Content-Sha256 into the string to sign but never hashes the body,
// so content substituted between signing and delivery was stored and read back
// without complaint.
//
// The check is streamed: the body is wrapped, the digest is finished when the
// last byte arrives, and a mismatch is reported as a read error so the storage
// layer aborts instead of committing bytes nobody signed. Buffering the body
// instead would cap verification at a few megabytes, which left every large
// upload unchecked.
// decodedLengthKey carries x-amz-decoded-content-length so the backend can check
// that the framed body really decoded to the number of bytes it promised.
type decodedLengthKey struct{}

func decodedContentLength(ctx context.Context) int64 {
	length, _ := ctx.Value(decodedLengthKey{}).(int64)
	return length
}

// streamingPayloadSupported lists the aws-chunked framings gofakes3 decodes.
// Anything else that declares a streaming body would be handed to the storage
// as if the chunk framing were the content, so it is refused instead.
var streamingPayloadSupported = map[string]string{
	"STREAMING-AWS4-HMAC-SHA256-PAYLOAD":         "STREAMING-AWS4-HMAC-SHA256-PAYLOAD",
	"STREAMING-UNSIGNED-PAYLOAD-TRAILER":         "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
	"STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER": "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
}

func contentSha256Guard(next http.Handler) http.Handler { return contentSha256GuardWithKeys(next, nil) }

func contentSha256GuardWithKeys(next http.Handler, keys *keyRing) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawSha := strings.TrimSpace(r.Header.Get("X-Amz-Content-Sha256"))
		upper := strings.ToUpper(rawSha)
		decodedLen := strings.TrimSpace(r.Header.Get("X-Amz-Decoded-Content-Length"))
		streaming := strings.HasPrefix(upper, "STREAMING-") || decodedLen != "" ||
			strings.Contains(strings.ToLower(r.Header.Get("Content-Encoding")), "aws-chunked")
		if streaming {
			// gofakes3 compares the header against two exact spellings and
			// silently skips de-chunking for everything else, which stores the
			// framing bytes as the object. Unknown spellings are refused, and the
			// ones it does understand are normalised (the comparison is case
			// sensitive, clients do send lowercase).
			canonical, ok := streamingPayloadSupported[upper]
			if !ok {
				writeS3Error(w, r, http.StatusBadRequest, "NotImplemented", bucketOnlyName(r.URL.Path),
					"the streaming payload form "+strconv.Quote(rawSha)+" is not supported")
				return
			}
			// enableSignatureVerification hashes X-Amz-Content-Sha256 as the
			// signed payload value, so the header may only be rewritten to the
			// spelling gofakes3 understands after the seed was verified:
			// renaming the signed variant first broke the seed check for every
			// legitimately signed streaming upload.
			if encoded, err := strconv.ParseInt(decodedLen, 10, 64); err == nil && encoded >= 0 {
				r = r.WithContext(context.WithValue(r.Context(), decodedLengthKey{}, encoded))
				// gofakes3 caps its frame decoder at this length, so a body that
				// carries more bytes than it announced is cut off upstream: the
				// storage layer only ever sees the announced prefix and putStream
				// stores it with 200 OK. The frame headers are counted here,
				// above that limit, so the overrun is refused instead.
				watcher := newChunkFrameWatcher(r.Body, encoded, r.Header.Get("X-Amz-Trailer"))
				// Both signed streaming variants carry a SigV4 chunk chain; the
				// -TRAILER one adds a trailer section after the zero chunk, but
				// its frames are signed the same way, so treating it as unsigned
				// let a tampered frame through with 200 OK.
				if upper == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD" ||
					upper == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER" {
					if err := watcher.enableSignatureVerification(r, keys); err != nil {
						writeS3Error(w, r, http.StatusForbidden, "SignatureDoesNotMatch", bucketOnlyName(r.URL.Path), err.Error())
						return
					}
				}
				r.Body = watcher
				r = r.WithContext(context.WithValue(r.Context(), chunkWatcherKey{}, watcher))
			}
			if rawSha != canonical {
				r.Header.Set("X-Amz-Content-Sha256", canonical)
				// gofakes3 re-verifies the seed inside its own auth middleware
				// against the rewritten header; re-sign the request so that
				// check sees a signature matching the bytes it verifies. The
				// client was already authenticated against the original bytes
				// by enableSignatureVerification above.
				if upper == "STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER" {
					if secret, ok := keys.secret(extractAccessKey(r)); ok {
						resignSeedAuthorization(r, secret)
					}
				}
			}
			next.ServeHTTP(w, r)
			return
		}

		declared := strings.ToLower(rawSha)
		if r.Body == nil || r.Body == http.NoBody || len(declared) != 64 || !isHexString(declared) {
			// UNSIGNED-PAYLOAD and the aws-chunked forms do not describe the
			// bytes on the wire, so there is nothing to compare against.
			next.ServeHTTP(w, r)
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), payloadHashKey{}, declared))
		r.Body = &hashingBody{inner: r.Body, want: declared, sum: sha256.New()}
		next.ServeHTTP(w, r)
	})
}

// hashingBody verifies a declared payload hash while the body streams past it.
type hashingBody struct {
	inner io.ReadCloser
	want  string
	sum   hash.Hash
	done  bool
	bad   bool
}

func (b *hashingBody) Read(p []byte) (int, error) {
	if b.bad {
		return 0, errPayloadHashMismatch
	}
	n, err := b.inner.Read(p)
	if n > 0 {
		_, _ = b.sum.Write(p[:n])
	}
	if err == io.EOF && !b.done {
		b.done = true
		if hex.EncodeToString(b.sum.Sum(nil)) != b.want {
			b.bad = true
			// The bytes read are handed over, the error is what stops the
			// write: the storage layer sees a failing body and discards the
			// partial object instead of publishing it.
			return n, errPayloadHashMismatch
		}
	}
	return n, err
}

func (b *hashingBody) Close() error { return b.inner.Close() }

func isHexString(v string) bool {
	for _, c := range v {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f':
		default:
			return false
		}
	}
	return true
}

// partNumberGuard refuses the `partNumber` parameter on a read.
//
// gofakes3 ignores it and answers with the whole object, so a client that asked
// for part 2 of a multipart object receives part 1's bytes at the offset it
// expected part 2 at - a silently corrupted download. Supporting it properly
// needs the part boundaries of the finished object, which are not recorded, so
// the honest answer is to say it is not implemented and let the client fall
// back to a byte `Range`, which is fully supported.
// multipartQueryGuard refuses an object request whose multipart query
// parameters cannot name an upload.
//
// gofakes3 routes on the *presence* of "?partNumber" and "?uploadId" rather than
// on their values, so a request that carries one of them with an empty upload id
// falls through to the ordinary object handler: a client that lost its upload id
// silently overwrote the whole object with a single part (PUT) or deleted the
// object outright (DELETE, where "?uploadId=" was read as an abort that then ran
// the plain delete). S3 answers 400 InvalidArgument in every one of those cases,
// and nothing else in the chain notices because the request is well formed as an
// ordinary object request.
func multipartQueryGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		uploadID := query.Get("uploadId")
		_, hasUploadID := query["uploadId"]
		_, hasPartNumber := query["partNumber"]
		switch {
		case hasUploadID && uploadID == "":
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", "",
				"a multipart request carried an empty uploadId; S3 requires a non-empty upload id.")
			return
		case hasPartNumber && uploadID == "":
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", "",
				"a partNumber was given without an uploadId; uploading a part requires both.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func partNumberGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := r.URL.Query()["partNumber"]; !ok {
			next.ServeHTTP(w, r)
			return
		}
		writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented",
			bucketOnlyName(r.URL.Path),
			"reading a single part by partNumber is not implemented; use a byte Range request instead.")
	})
}

// lowercaseUserMetadataHeaders rewrites user metadata response headers into the
// shape AWS sends. gofakes3 writes them with Header().Set, and Go canonicalises
// header names on the way in, so a GET answered "X-Amz-Meta-Team" while the
// client had stored "team": boto3 exposes the suffix verbatim, and
// obj["Metadata"]["team"] came back empty. Amazon documents that user metadata
// keys are lowercase, so the response is normalised to match.
func lowercaseUserMetadataHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lw := &lowercaseMetadataWriter{ResponseWriter: w}
		next.ServeHTTP(lw, r)
		// A HEAD response carries no body, and gofakes3 fills in the object
		// headers after the status is decided, so the map is only complete once
		// the handler has returned. Normalising here as well covers that shape.
		lowercaseUserMetadataKeys(lw.Header())
	})
}

type lowercaseMetadataWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *lowercaseMetadataWriter) WriteHeader(code int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		lowercaseUserMetadataKeys(w.Header())
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *lowercaseMetadataWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush keeps streaming responses working through the wrapper.
func (w *lowercaseMetadataWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		if !w.wroteHeader {
			w.WriteHeader(http.StatusOK)
		}
		f.Flush()
	}
}

func lowercaseUserMetadataKeys(header http.Header) {
	for key, value := range header {
		if len(key) <= len(userMetadataPrefix) ||
			!strings.EqualFold(key[:len(userMetadataPrefix)], userMetadataPrefix) {
			continue
		}
		lower := userMetadataPrefix + strings.ToLower(key[len(userMetadataPrefix):])
		if lower == key {
			continue
		}
		delete(header, key)
		header[lower] = value
	}
}

// expectedBucketOwnerGuard honours `x-amz-expected-bucket-owner` and
// `x-amz-expected-source-bucket-owner`. S3 answers 403 when the named bucket is
// not the one the client expected; ignoring the header silently drops a safety
// net that clients set so that a wrong endpoint or a DNS surprise cannot write
// into another bucket.
func expectedBucketOwnerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bucket := bucketOnlyName(r.URL.Path)
		if bucket != "" {
			if expected := strings.TrimSpace(r.Header.Get("X-Amz-Expected-Bucket-Owner")); expected != "" && expected != bucket {
				writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket,
					"The bucket you are attempting to access must be addressed using the expected bucket name.")
				return
			}
			if source := copySourceBucketName(r); source != "" {
				if expected := strings.TrimSpace(r.Header.Get("X-Amz-Expected-Source-Bucket-Owner")); expected != "" && expected != source {
					writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket,
						"The source bucket you are attempting to access must be addressed using the expected bucket name.")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// copySourceBucketName returns the bucket named by `x-amz-copy-source`, if any.
func copySourceBucketName(r *http.Request) string {
	source := strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source"))
	if source == "" {
		return ""
	}
	source = strings.TrimPrefix(source, "/")
	source = strings.SplitN(source, "/", 2)[0]
	if idx := strings.IndexByte(source, '?'); idx >= 0 {
		source = source[:idx]
	}
	return source
}

// storageClasses names the `x-amz-storage-class` values S3 accepts. The gateway
// stores everything itself, so the class has no effect, but a client that sends
// a misspelled class must not be told the object was stored with it: AWS
// answers 400 InvalidStorageClass.
var storageClasses = map[string]bool{
	"STANDARD":            true,
	"REDUCED_REDUNDANCY":  true,
	"STANDARD_IA":         true,
	"ONEZONE_IA":          true,
	"INTELLIGENT_TIERING": true,
	"GLACIER":             true,
	"DEEP_ARCHIVE":        true,
	"GLACIER_IR":          true,
	"EXPRESS_ONEZONE":     true,
	"OUTPOSTS":            true,
	"SNOW":                true,
}

// storageClassGuard rejects an unknown storage class on a write.
func storageClassGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			if class := strings.TrimSpace(r.Header.Get("X-Amz-Storage-Class")); class != "" && !storageClasses[class] {
				writeS3Error(w, r, http.StatusBadRequest, "InvalidStorageClass", bucketOnlyName(r.URL.Path),
					"Invalid storage class specified.")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ownerACLs are the canned ACLs that describe exactly the access the gateway
// already grants (the object's own bucket credentials). Anything that promises
// more — public-read, authenticated-read, public-read-write — cannot be honoured
// because object ACLs are not implemented, and accepting the header would tell
// the client it had published something it had not.
var ownerACLs = map[string]bool{
	"private":                   true,
	"bucket-owner-read":         true,
	"bucket-owner-full-control": true,
}

// unsupportedWriteHeaderGuard refuses write headers whose effect this gateway
// cannot provide, instead of storing the request and dropping the promise.
func unsupportedWriteHeaderGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut || r.Method == http.MethodPost {
			if tagging := strings.TrimSpace(r.Header.Get("X-Amz-Tagging")); tagging != "" {
				writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented", bucketOnlyName(r.URL.Path),
					"Object tagging is not implemented by this gateway, so x-amz-tagging cannot be stored.")
				return
			}
			if acl := strings.TrimSpace(r.Header.Get("X-Amz-Acl")); acl != "" && !ownerACLs[acl] {
				writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented", bucketOnlyName(r.URL.Path),
					"Object ACLs are not implemented by this gateway, so "+acl+" cannot be granted.")
				return
			}
			for name := range r.Header {
				if strings.HasPrefix(strings.ToLower(name), "x-amz-object-lock-") {
					writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented", bucketOnlyName(r.URL.Path),
						"Object lock is not implemented by this gateway.")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// signatureScopedHeaderNames and signatureScopedHeaderPrefixes list the request
// headers that decide *who* may read or change an object rather than merely
// describing it: ACL and grant headers, the object-lock trio, the website
// redirect target and the server-side-encryption choices.
//
// They are dangerous in a different way from the copy-source headers: a signed
// request is authorized by its Credential, and the signature only vouches for
// the headers named in SignedHeaders. gofakes3 nevertheless applies whatever it
// finds in the request, so anyone who can see a signed request on its way to
// the gateway - or who is handed a presigned URL - can attach `X-Amz-Acl:
// public-read` and publish an object whose key was only allowed to write it.
var signatureScopedHeaderNames = []string{
	"x-amz-acl",
	"x-amz-object-lock-legal-hold",
	"x-amz-object-lock-mode",
	"x-amz-object-lock-retain-until-date",
	"x-amz-server-side-encryption",
	"x-amz-server-side-encryption-customer-algorithm",
	"x-amz-server-side-encryption-customer-key",
	"x-amz-server-side-encryption-customer-key-md5",
	"x-amz-website-redirect-location",
}

var signatureScopedHeaderPrefixes = []string{"x-amz-grant-"}

// presentSignatureScopedHeaders returns the lower-case names of the
// signature-scoped headers this request carries, in a stable order.
func presentSignatureScopedHeaders(r *http.Request) []string {
	var present []string
	for name := range r.Header {
		lower := strings.ToLower(name)
		scoped := false
		for _, exact := range signatureScopedHeaderNames {
			if lower == exact {
				scoped = true
				break
			}
		}
		if !scoped {
			for _, prefix := range signatureScopedHeaderPrefixes {
				if strings.HasPrefix(lower, prefix) {
					scoped = true
					break
				}
			}
		}
		if scoped {
			present = append(present, lower)
		}
	}
	for i := 1; i < len(present); i++ {
		for j := i; j > 0 && present[j] < present[j-1]; j-- {
			present[j], present[j-1] = present[j-1], present[j]
		}
	}
	return present
}

// signatureCoversHeaders reports whether the request's signature authenticates
// every one of the named headers.
//
// Signature V2 hashes every x-amz- header into the string to sign, so a V2
// request cannot carry one its signer did not see; a request with no credential
// at all has nothing to vouch for the header with, which is why the caller
// answers such a request before reaching here. Everything else is Signature V4,
// which canonicalises exactly the headers named in SignedHeaders (header form)
// or X-Amz-SignedHeaders (presigned form).
func signatureCoversHeaders(r *http.Request, names []string) bool {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	var signed string
	switch {
	case auth != "" && !strings.HasPrefix(auth, "AWS4-"):
		return true
	case auth != "":
		signed = authorizationSignedHeaders(auth)
		if signed == "" {
			return false
		}
	case r.URL.Query().Get("AWSAccessKeyId") != "" && r.URL.Query().Get("Signature") != "":
		return true
	case r.URL.Query().Get("X-Amz-Signature") != "":
		signed = r.URL.Query().Get("X-Amz-SignedHeaders")
	default:
		return false
	}
	signedSet := make(map[string]bool)
	for _, name := range strings.Split(strings.ToLower(signed), ";") {
		signedSet[strings.TrimSpace(name)] = true
	}
	for _, name := range names {
		if !signedSet[name] {
			return false
		}
	}
	return true
}

// headerSignGuard refuses a signed request that carries a signature-scoped
// header the signature does not cover.
//
// Only requests with a credential are judged here. A request without one is
// already answered downstream - `unsupportedWriteHeaderGuard` refuses these
// headers outright (501), and the bucket rules decide the rest - so pre-empting
// that with a 403 here would only replace one refusal with a worse-described
// one. The attack this guard closes needs a credential: a valid signature that
// the extra header is not part of.
func headerSignGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scoped := presentSignatureScopedHeaders(r)
		if len(scoped) == 0 || !hasAnyCredential(r) {
			next.ServeHTTP(w, r)
			return
		}
		bucket, _, _ := parseObjectPath(r.URL.Path)
		if bucket == "" {
			bucket = bucketOnlyName(r.URL.Path)
		}
		if !signatureCoversHeaders(r, scoped) {
			writeS3Error(w, r, http.StatusForbidden, "AccessDenied", bucket,
				scoped[0]+" must be covered by the signature (list it in SignedHeaders / X-Amz-SignedHeaders).")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasAnyCredential reports whether the request carries a credential of any
// supported shape: a V2 or V4 Authorization header, or a V2 or V4 presigned
// query.
func hasAnyCredential(r *http.Request) bool {
	if strings.TrimSpace(r.Header.Get("Authorization")) != "" {
		return true
	}
	query := r.URL.Query()
	return query.Get("X-Amz-Signature") != "" ||
		(query.Get("AWSAccessKeyId") != "" && query.Get("Signature") != "")
}

// responseOverrideParams are the query parameters that rewrite response headers
// (`?response-content-type=…` and friends). S3 honours them only on a signed
// request: a presigned URL carries them inside the signature, so the signer
// chose them. An anonymous request cannot, and honouring it anyway would let
// anyone turn a stored `text/plain` object into `text/html` served from this
// origin - a stored-XSS primitive that needs no write access at all.
var responseOverrideParams = []string{
	"response-content-type",
	"response-content-language",
	"response-expires",
	"response-cache-control",
	"response-content-disposition",
	"response-content-encoding",
}

func responseOverrideName(name string) bool {
	for _, candidate := range responseOverrideParams {
		if candidate == name {
			return true
		}
	}
	return false
}

// isV2Credential reports whether the request is authenticated with the SigV2
// scheme, either by header or by a presigned query string.
func isV2Credential(r *http.Request) bool {
	if authorization := strings.TrimSpace(r.Header.Get("Authorization")); authorization != "" {
		return strings.HasPrefix(authorization, "AWS ") && !strings.HasPrefix(authorization, "AWS4-")
	}
	query := r.URL.Query()
	return query.Get("AWSAccessKeyId") != "" && query.Get("Signature") != ""
}

// responseOverrideGuard keeps the `response-*` response-header rewrites inside
// the signature that authorises them.
//
// Three cases, because the two signing schemes bind different things:
//
//   - no credential at all: refused with 400 InvalidRequest, the same answer S3
//     gives. Honouring it would let anyone serve a stored `text/plain` object as
//     `text/html` from this origin, which is a stored-XSS primitive needing no
//     write access whatsoever.
//   - SigV4 (header or presigned): every query parameter is part of the
//     canonical request, so the signer necessarily chose these parameters -
//     nothing to do here.
//   - SigV2: the string-to-sign only covers the resource and a fixed list of
//     sub-resources, so `response-*` is never covered no matter how the request
//     was signed. Appending the parameter to an old presigned link would choose
//     the served Content-Type without any authorisation, so it is refused with
//     the same 400 instead of being honoured. Silently dropping the parameter
//     was the alternative, but an answered 200 would hide from the client that
//     the header it asked for is not the header it gets.
func responseOverrideGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		overridden := false
		for _, name := range responseOverrideParams {
			if _, ok := query[name]; ok {
				overridden = true
				break
			}
		}
		if overridden && (!hasAnyCredential(r) || isV2Credential(r)) {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidRequest", bucketOnlyName(r.URL.Path),
				"Request specific response headers cannot be used for anonymous requests.")
			return
		}
		next.ServeHTTP(w, r)
	})
}
