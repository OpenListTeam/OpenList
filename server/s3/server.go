// Credits: https://pkg.go.dev/github.com/rclone/rclone@v1.65.2/cmd/serve/s3
// Package s3 implements a fake s3 server for openlist
package s3

import (
	"bytes"
	"context"
	"encoding/xml"
	"math/rand"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/OpenListTeam/gofakes3"
)

// Make a new S3 Server to serve the remote
func NewServer(ctx context.Context) (h http.Handler, err error) {
	var newLogger logger
	// The ring is seeded from the settings once and then kept in sync while the
	// process runs, so adding or rotating an access key in the admin UI does not
	// require a restart.
	keys := newKeyRing(resolveAuthPairs())
	backend := newBackend().(*s3Backend)
	faker := gofakes3.New(
		backend,
		// gofakes3.WithHostBucket(!opt.pathBucketMode),
		gofakes3.WithLogger(newLogger),
		gofakes3.WithRequestID(rand.Uint64()),
		gofakes3.WithoutVersioning(),
		gofakes3.WithV4Auth(keys.snapshot()),
		gofakes3.WithIntegrityCheck(true), // Check Content-MD5 if supplied
	)

	// A second, deliberately keyless instance serves anonymous reads of buckets
	// flagged public. gofakes3 accepts every request when it has no keys, so the
	// only way to allow anonymous reads without opening writes is to give them
	// their own instance and gate it in front.
	publicBackend := newBackend().(*s3Backend)
	publicFaker := gofakes3.New(
		publicBackend,
		gofakes3.WithLogger(newLogger),
		gofakes3.WithRequestID(rand.Uint64()),
		gofakes3.WithoutVersioning(),
		gofakes3.WithIntegrityCheck(true), // Check Content-MD5 if supplied
	)

	keys.bind(faker)
	go keys.watch(ctx)

	// corsHandler wraps objectKeyGuard, not the other way round: a request whose
	// key walks out of its bucket is refused with 400, and a browser can only
	// read that answer - or anything else - when the CORS headers are already
	// applied. Inside it, the redirect handler resolves paths against storage
	// long before gofakes3 would validate anything, which is why the guard runs
	// before it.
	// presignedLifetimeGuard sits outside corsHandler: the `?cors` branch answers
	// without calling next, so a guard further in never ran for it and a
	// presigned CORS URL could be minted with no expiry ceiling at all.
	// contentSha256Guard sits outside corsHandler for the same reason: the `?cors`
	// branch reads its document without calling next, so a PUT carrying a signed
	// payload hash used to reach storage with whatever bytes arrived. It wraps
	// every other body-carrying request as well, which is why it is no longer
	// repeated further in.
	payloadGuard := func(next http.Handler) http.Handler { return contentSha256GuardWithKeys(next, keys) }
	// The outermost wrapper lowercases user metadata headers, which Go
	// canonicalises on the way in and gofakes3 re-emits with Header().Set.
	return multipartQueryGuard(responseOverrideGuard(headerSignGuard(echoDeclaredChecksum(lowercaseUserMetadataHeaders(presignedLifetimeGuard(payloadGuard(corsHandler(objectKeyGuard(expectedBucketOwnerGuard(storageClassGuard(unsupportedWriteHeaderGuard(partNumberBoundsGuard(metadataDirectiveGuard(copySourceRangeGuard(partNumberGuard(validateListQuery(limitMultiDeleteBody(multiDeleteDigestGuard(redirectHandler(bucketAuthHandler(
		repairS3Response(fixListEncoding(unsupportedSubresources(stripUnrequestedChecksums(stripRangeChecksums(rangeStatusHandler(multiRangeGuard(deleteConditionGuard(copySourceConditionGuard(listPartsHandler(uploadPartCopyHandler(withPutPreconditions(ifRangeHolder(backend, faker.Server())), backend, keys), backend), keys, backend.baseCopySourceIdentity), keys, backend.baseDeleteIdentity), backend, keys))))))),
		publicBucketGuard(repairS3Response(fixListEncoding(unsupportedSubresources(stripUnrequestedChecksums(stripRangeChecksums(rangeStatusHandler(multiRangeGuard(withPutPreconditions(ifRangeHolder(publicBackend, publicFaker.Server())), publicBackend, keys)))))))),
	), keys, backend)))))))))))))))))))), nil
}

// limitMultiDeleteBody bounds the body of a `POST /bucket?delete`. gofakes3
// decodes that document straight from the request stream without a limit, so a
// signed caller could otherwise make the process buffer an arbitrary amount of
// XML.
func limitMultiDeleteBody(next http.Handler) http.Handler {
	const maxBody = 1 << 20 // 1 MiB: a DeleteObjects document with ~10k keys
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil {
			if _, ok := r.URL.Query()["delete"]; ok {
				r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// checksumHeaderNames lists the response headers that describe a whole object.
var checksumHeaderNames = []string{
	"X-Amz-Checksum-Crc32",
	"X-Amz-Checksum-Crc32c",
	"X-Amz-Checksum-Crc64nvme",
	"X-Amz-Checksum-Sha1",
	"X-Amz-Checksum-Sha256",
	"X-Amz-Checksum-Type",
}

// stripRangeChecksums drops the whole-object checksum headers from any response
// that does not carry the whole object.
//
// The stored checksum describes the complete content, so a client that asks for
// a range (or gets a conditional answer, or an error) and validates the echoed
// value fails every time - which is exactly what the AWS SDKs do once checksum
// mode is on. A 304 is the worst case: botocore wraps the (empty) body in a
// StreamingChecksumBody and then tries to parse it as an XML error document,
// raising TypeError instead of reporting "not modified". Real S3 returns those
// headers only on a full 200 response.
func stripRangeChecksums(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(&rangeChecksumWriter{ResponseWriter: w}, r)
	})
}

type rangeChecksumWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *rangeChecksumWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if status != http.StatusOK {
			for _, name := range checksumHeaderNames {
				w.Header().Del(name)
			}
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *rangeChecksumWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// unsupportedSubresources answers the S3 subresources this gateway does not
// implement with 501 instead of letting them fall through to the object
// handlers.
//
// A subresource such as ?acl or ?policy used to reach the listing handler,
// which answered 200 with a ListBucketResult: a client asking for its bucket
// policy got a listing, parsed as a policy document, and saw a nonsense
// configuration that looked real. NotImplemented says the truth.
var unsupportedSubresourceNames = map[string]bool{
	"acl":                 true,
	"tagging":             true,
	"lifecycle":           true,
	"policy":              true,
	"policyStatus":        true,
	"logging":             true,
	"encryption":          true,
	"object-lock":         true,
	"retention":           true,
	"legal-hold":          true,
	"restore":             true,
	"select":              true,
	"torrent":             true,
	"attributes":          true,
	"replication":         true,
	"notification":        true,
	"website":             true,
	"requestPayment":      true,
	"accelerate":          true,
	"analytics":           true,
	"inventory":           true,
	"metrics":             true,
	"intelligent-tiering": true,
	"ownershipControls":   true,
	"publicAccessBlock":   true,
}

func unsupportedSubresources(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		for name := range unsupportedSubresourceNames {
			if _, ok := query[name]; !ok {
				continue
			}
			writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented",
				bucketOnlyName(r.URL.Path),
				"The S3 subresource ?"+name+" is not implemented by this gateway.")
			return
		}
		// Versioning is not implemented, so a request that names a specific
		// version cannot be honoured. GET and DELETE already answered
		// NotImplemented here, but HEAD slipped through and replied 200 with the
		// current object, and `x-amz-copy-source: bucket/key?versionId=x` was
		// silently copied as the current object: both told the client it had
		// asked for something it did not get.
		if version := query.Get("versionId"); version != "" && version != "null" {
			writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented",
				bucketOnlyName(r.URL.Path),
				"Versioning is not implemented by this gateway, so a specific version cannot be requested.")
			return
		}
		if source := r.Header.Get("X-Amz-Copy-Source"); strings.Contains(source, "versionId=") {
			writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented",
				bucketOnlyName(r.URL.Path),
				"Versioning is not implemented by this gateway, so a specific source version cannot be copied.")
			return
		}
		// In-progress uploads are kept in memory and cannot be listed yet; say so
		// rather than answering NoSuchUpload for a bucket-level listing.
		if _, ok := query["uploads"]; ok && r.Method == http.MethodGet && !hasObjectKey(r.URL.Path) {
			writeS3Error(w, r, http.StatusNotImplemented, "NotImplemented",
				bucketOnlyName(r.URL.Path),
				"Listing in-progress multipart uploads is not implemented by this gateway.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasObjectKey reports whether the request path names an object below a bucket.
func hasObjectKey(rawPath string) bool {
	trimmed := strings.TrimPrefix(rawPath, "/")
	if i := strings.Index(trimmed, "/"); i >= 0 {
		return strings.Trim(trimmed[i+1:], "/") != ""
	}
	return false
}

// validateListQuery rejects list parameters that S3 rejects.
//
// gofakes3 parses max-keys as an int and accepts the result, so "-5" produced an
// empty page with IsTruncated=false: a client that computed a negative limit
// saw an empty bucket and might then delete its local copy to match. An unknown
// list-type was silently served as version 1.
func validateListQuery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		bucket := bucketOnlyName(r.URL.Path)
		if raw := query.Get("max-keys"); raw != "" {
			if n, err := strconv.Atoi(raw); err != nil || n < 0 || n > 2147483647 {
				writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
					"max-keys must be an integer between 0 and 2147483647.")
				return
			}
		}
		if listType := query.Get("list-type"); listType != "" && listType != "1" && listType != "2" {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
				"list-type must be 1 or 2.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// s3ListEncodingBufferLimit bounds how much of a listing is buffered for the
// encoding-type=url fix. A page is 1000 keys, so real responses are far below
// it; a larger body is passed through untouched instead of being buffered.
const s3ListEncodingBufferLimit = 8 << 20

// listEncodeFixup matches the XML elements that carry object names. Only
// <Key> and <Prefix> are rewritten: gofakes3 URL-encodes exactly those.
var listEncodeFixup = regexp.MustCompile(`<(Key|Prefix)>([^<]*)</(?:Key|Prefix)>`)

// fixPercentEncodedListing rewrites the space encoding of a listing answered
// with encoding-type=url.
//
// gofakes3 encodes object names with a helper that renders a space as "+",
// while S3 renders it as "%20". SDKs decode the value with percent-decoding,
// which leaves "+" alone, so a boto3 client listing this gateway saw
// "my file.txt" as "my+file.txt": the object looked missing, and a sync tool
// could then delete the local copy or upload the same content twice. A literal
// "+" in a name is already encoded as "%2B", so every bare "+" inside these
// elements is a space.
func fixPercentEncodedListing(body []byte) []byte {
	return listEncodeFixup.ReplaceAllFunc(body, func(match []byte) []byte {
		if !bytes.ContainsRune(match, '+') {
			return match
		}
		return bytes.ReplaceAll(match, []byte("+"), []byte("%20"))
	})
}

// nextMarkerFixup matches the v1 paging cursor. gofakes3 writes it verbatim
// while the rest of the document is URL-encoded.
var nextMarkerFixup = regexp.MustCompile(`<NextMarker>([^<]*)</NextMarker>`)

// fixNextMarkerEncoding URL-encodes the v1 <NextMarker> of a listing answered
// with encoding-type=url.
//
// gofakes3 encodes <Key> and <Prefix> but copies <NextMarker> straight from the
// backend, so a key containing "+" came back as "a+b/" while the client decoded
// the whole document with unquote_plus and turned it into "a b/". The cursor
// then sorts before the prefix it was meant to skip, the server sends the same
// prefix again, and a paginating client loops for ever while never seeing the
// keys that sort after it - aws s3 ls/sync, barman-cloud and every botocore
// caller were stuck in that loop.
func fixNextMarkerEncoding(body []byte) []byte {
	return nextMarkerFixup.ReplaceAllFunc(body, func(match []byte) []byte {
		sub := nextMarkerFixup.FindSubmatch(match)
		if len(sub) != 2 || len(sub[1]) == 0 {
			return match
		}
		decoded := xmlTextDecode(string(sub[1]))
		if decoded == "" {
			return match
		}
		encoded := strings.ReplaceAll(gofakes3.URLEncode(decoded), "+", "%20")
		return []byte("<NextMarker>" + encoded + "</NextMarker>")
	})
}

// xmlTextDecode resolves the XML entities the encoder may have produced, so
// that a key holding "&" or "<" is re-encoded from its real value instead of
// being escaped twice.
func xmlTextDecode(s string) string {
	var out struct {
		Text string `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte("<v>"+s+"</v>"), &out); err != nil {
		return ""
	}
	return out.Text
}

// markerFixup matches the v1 <Marker> echo. gofakes3 copies it straight from
// the request while the rest of the document is URL-encoded.
var markerFixup = regexp.MustCompile(`<Marker>([^<]*)</Marker>`)

// fixMarkerEncoding URL-encodes the v1 <Marker> echo of a listing answered with
// encoding-type=url.
//
// gofakes3 encodes <Key>, <Prefix> and <Delimiter> but copies <Marker> from the
// request as-is. A client that asks for encoding-type=url decodes every string
// in the document, so a marker holding a literal "%20" (a key named "a%20b")
// came back raw, was decoded again on the client side to "a b", and the next
// page was requested from a marker that does not exist in the bucket - the
// listing silently skipped or repeated keys. The same asymmetry is why
// <NextMarker> needed fixing above.
func fixMarkerEncoding(body []byte) []byte {
	return markerFixup.ReplaceAllFunc(body, func(match []byte) []byte {
		sub := markerFixup.FindSubmatch(match)
		if len(sub) != 2 || len(sub[1]) == 0 {
			return match
		}
		decoded := xmlTextDecode(string(sub[1]))
		if decoded == "" {
			return match
		}
		return []byte("<Marker>" + strings.ReplaceAll(gofakes3.URLEncode(decoded), "+", "%20") + "</Marker>")
	})
}

// fixListEncoding buffers a URL-encoded listing and repairs it in place.
func fixListEncoding(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Query().Get("encoding-type") != "url" || hasObjectKey(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		recorder := &listEncodingRecorder{ResponseWriter: w, limit: s3ListEncodingBufferLimit}
		next.ServeHTTP(recorder, r)
		recorder.finish()
	})
}

type listEncodingRecorder struct {
	http.ResponseWriter
	status   int
	buf      bytes.Buffer
	limit    int
	overflow bool
	wrote    bool
}

func (r *listEncodingRecorder) WriteHeader(status int) {
	if r.wrote || r.overflow {
		return
	}
	r.status = status
	r.wrote = true
}

func (r *listEncodingRecorder) Write(p []byte) (int, error) {
	if r.overflow {
		return r.ResponseWriter.Write(p)
	}
	if r.buf.Len()+len(p) > r.limit {
		// Too large to repair: emit what we have and pass the rest through.
		r.overflow = true
		r.flushHeader()
		if _, err := r.ResponseWriter.Write(r.buf.Bytes()); err != nil {
			r.buf.Reset()
			return 0, err
		}
		r.buf.Reset()
		return r.ResponseWriter.Write(p)
	}
	return r.buf.Write(p)
}

func (r *listEncodingRecorder) flushHeader() {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *listEncodingRecorder) finish() {
	if r.overflow {
		return
	}
	if r.status != 0 && r.status != http.StatusOK {
		// Errors are not listings; forward them unchanged.
		r.ResponseWriter.Header().Set("Content-Length", strconv.Itoa(r.buf.Len()))
		r.flushHeader()
		_, _ = r.ResponseWriter.Write(r.buf.Bytes())
		return
	}
	body := fixNextMarkerEncoding(r.buf.Bytes())
	body = fixMarkerEncoding(body)
	body = fixPercentEncodedListing(body)
	r.ResponseWriter.Header().Set("Content-Length", strconv.Itoa(len(body)))
	r.flushHeader()
	_, _ = r.ResponseWriter.Write(body)
}
