package s3

import (
	"bytes"
	"context"
	"net/http"
	"strconv"
	"strings"

	gofakes3 "github.com/OpenListTeam/gofakes3"
)

// The repairs in this file all exist because gofakes3 builds its responses from
// structs that carry no such field, so the only place to fix them is the wire
// format. They were found by driving the gateway with a real boto3 client
// (botocore 1.43, checksum mode on by default), which reads these fields and
// behaves differently when they are missing:
//
//   - an empty ListObjectsV2 answer without KeyCount makes paginating clients
//     treat the page as "unknown size" instead of "empty";
//   - CompleteMultipartUpload with an empty Location leaves the SDK without a
//     URL for the object it just created;
//   - a checksum header the client did not ask for makes botocore wrap the body
//     in a validating stream and fail on responses that are not the whole
//     object.

// repairS3Response buffers listing and CompleteMultipartUpload responses and
// repairs the fields gofakes3 cannot fill in.
func repairS3Response(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isRepairableResponse(r) {
			next.ServeHTTP(w, r)
			return
		}
		recorder := &responseRepairRecorder{
			ResponseWriter: w,
			limit:          s3ListEncodingBufferLimit,
			encodingURL:    r.URL.Query().Get("encoding-type") == "url",
			listV2:         r.URL.Query().Get("list-type") == "2",
			complete:       isCompleteMultipartUpload(r),
			location:       "http://" + r.Host + r.URL.Path,
		}
		next.ServeHTTP(recorder, r)
		recorder.finish()
	})
}

// isRepairableResponse reports whether the response to r is one of the bodies
// this middleware understands.
func isRepairableResponse(r *http.Request) bool {
	if hasObjectKey(r.URL.Path) {
		// Object bodies are streamed: a listing never has a key in the path.
		return isCompleteMultipartUpload(r)
	}
	return r.Method == http.MethodGet || r.Method == http.MethodHead || isCompleteMultipartUpload(r)
}

// isCompleteMultipartUpload reports whether r finishes a multipart upload.
func isCompleteMultipartUpload(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	_, ok := r.URL.Query()["uploadId"]
	return ok && hasObjectKey(r.URL.Path)
}

type responseRepairRecorder struct {
	http.ResponseWriter
	status      int
	buf         bytes.Buffer
	limit       int
	overflow    bool
	wrote       bool
	encodingURL bool
	listV2      bool
	complete    bool
	location    string
}

func (r *responseRepairRecorder) WriteHeader(status int) {
	if r.wrote || r.overflow {
		return
	}
	r.status = status
	r.wrote = true
}

func (r *responseRepairRecorder) Write(p []byte) (int, error) {
	if r.overflow {
		return r.ResponseWriter.Write(p)
	}
	if r.buf.Len()+len(p) > r.limit {
		// Too large to repair: flush what we have and stream the rest, so a
		// huge listing degrades to the upstream body instead of failing.
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

func (r *responseRepairRecorder) flushHeader() {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *responseRepairRecorder) finish() {
	if r.overflow {
		return
	}
	body := r.buf.Bytes()
	if r.status == 0 || r.status == http.StatusOK {
		body = repairResponseBody(body, r.encodingURL, r.listV2, r.complete, r.location)
	}
	r.ResponseWriter.Header().Set("Content-Length", strconv.Itoa(len(body)))
	r.flushHeader()
	_, _ = r.ResponseWriter.Write(body)
}

// repairResponseBody applies every textual repair to one buffered body.
func repairResponseBody(body []byte, encodingURL, listV2, complete bool, location string) []byte {
	out := body
	if listV2 {
		out = ensureKeyCount(out)
	}
	if encodingURL {
		out = encodeStartAfter(out)
	}
	if complete {
		out = fillCompleteLocation(out, location)
	}
	return out
}

// ensureKeyCount adds KeyCount to a ListObjectsV2 answer that lacks it.
//
// gofakes3 only fills KeyCount when it built the page from a non-empty result,
// so "prefix matched nothing" and "MaxKeys=0" came back without the element and
// clients that page by KeyCount saw no terminator.
func ensureKeyCount(body []byte) []byte {
	if !bytes.Contains(body, []byte("<ListBucketResult")) || bytes.Contains(body, []byte("<KeyCount>")) {
		return body
	}
	count := bytes.Count(body, []byte("<Contents>")) + bytes.Count(body, []byte("<CommonPrefixes>"))
	element := []byte("<KeyCount>" + strconv.Itoa(count) + "</KeyCount>")
	if idx := bytes.Index(body, []byte("<MaxKeys>")); idx >= 0 {
		out := make([]byte, 0, len(body)+len(element))
		out = append(out, body[:idx]...)
		out = append(out, element...)
		out = append(out, body[idx:]...)
		return out
	}
	if idx := bytes.Index(body, []byte("</ListBucketResult>")); idx >= 0 {
		out := make([]byte, 0, len(body)+len(element))
		out = append(out, body[:idx]...)
		out = append(out, element...)
		out = append(out, body[idx:]...)
		return out
	}
	return body
}

// encodeStartAfter URL-encodes StartAfter the way the other echoed values are
// encoded. gofakes3 encodes Key, Prefix, Delimiter and NextMarker when
// encoding-type=url but writes StartAfter verbatim, so a key holding a space or
// a "+" came back in a different form than the one the client sent.
func encodeStartAfter(body []byte) []byte {
	const open, close = "<StartAfter>", "</StartAfter>"
	start := bytes.Index(body, []byte(open))
	if start < 0 {
		return body
	}
	valueStart := start + len(open)
	end := bytes.Index(body[valueStart:], []byte(close))
	if end < 0 {
		return body
	}
	raw := string(body[valueStart : valueStart+end])
	decoded := xmlTextDecode(raw)
	if decoded == "" {
		return body
	}
	encoded := strings.ReplaceAll(gofakes3.URLEncode(decoded), "+", "%20")
	if encoded == raw {
		return body
	}
	out := make([]byte, 0, len(body)+len(encoded))
	out = append(out, body[:valueStart]...)
	out = append(out, encoded...)
	out = append(out, body[valueStart+end:]...)
	return out
}

// fillCompleteLocation fills the empty <Location> gofakes3 emits when a
// multipart upload finishes.
func fillCompleteLocation(body []byte, location string) []byte {
	if !bytes.Contains(body, []byte("<CompleteMultipartUploadResult")) {
		return body
	}
	if !bytes.Contains(body, []byte("<Location></Location>")) {
		return body
	}
	return bytes.Replace(body, []byte("<Location></Location>"),
		[]byte("<Location>"+xmlEscapeText(location)+"</Location>"), 1)
}

// stripUnrequestedChecksums drops x-amz-checksum-* from GET and HEAD answers
// that did not ask for them.
//
// S3 only returns the whole-object checksum when the request carries
// `x-amz-checksum-mode: ENABLED`. The gateway stores the value in object
// metadata, and gofakes3 writes metadata back as response headers, so every
// plain GET/HEAD advertised a checksum. botocore then validates the body
// against it: harmless for a full 200, but it also made clients treat the value
// as always-available and broke the bodies of responses that are not the whole
// object.
func stripUnrequestedChecksums(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		writer := &unrequestedChecksumWriter{
			ResponseWriter: w,
			dropChecksum:   !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Amz-Checksum-Mode")), "ENABLED"),
		}
		next.ServeHTTP(writer, r)
		// A HEAD answer is usually committed by net/http after the handler
		// returns, without either WriteHeader or Write being called on this
		// wrapper: the headers are still mutable here, and they have to be
		// dropped here or gofakes3's replay of the stored checksum reaches the
		// client on every plain HEAD.
		writer.drop()
	})
}

type unrequestedChecksumWriter struct {
	http.ResponseWriter
	dropChecksum bool
	wroteHeader  bool
}

func (w *unrequestedChecksumWriter) WriteHeader(status int) {
	w.drop()
	w.ResponseWriter.WriteHeader(status)
}

// Write drops the headers before the implicit 200 is committed: gofakes3 sets
// the object headers while building the response, so a check that ran before
// the handler would see an empty map.
func (w *unrequestedChecksumWriter) Write(p []byte) (int, error) {
	w.drop()
	return w.ResponseWriter.Write(p)
}

func (w *unrequestedChecksumWriter) drop() {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	// `x-amz-sdk-checksum-algorithm` describes the upload, not the object: S3
	// never sends it on GET/HEAD, whatever the checksum mode is.
	w.Header().Del("X-Amz-Sdk-Checksum-Algorithm")
	if !w.dropChecksum {
		return
	}
	for _, name := range checksumHeaderNames {
		w.Header().Del(name)
	}
}

// copySourceRangeGuard refuses the CopyObject form of a ranged copy.
//
// `x-amz-copy-source-range` is only valid on UploadPartCopy. Real S3 answers
// InvalidRequest on CopyObject, but gofakes3 reads none of the copy headers and
// copies the whole object, so a client that asked for 1 KiB of a 1 GiB object
// silently got all of it back as a new object.
func copySourceRangeGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("X-Amz-Copy-Source") == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("X-Amz-Copy-Source-Range") == "" {
			next.ServeHTTP(w, r)
			return
		}
		// UploadPartCopy carries the same header and is the request the header
		// exists for: only the plain CopyObject form is refused.
		if _, ok := r.URL.Query()["uploadId"]; ok {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := r.URL.Query()["partNumber"]; ok {
			next.ServeHTTP(w, r)
			return
		}
		writeS3Error(w, r, http.StatusBadRequest, "InvalidRequest",
			bucketOnlyName(r.URL.Path),
			"x-amz-copy-source-range is only supported by UploadPartCopy, not by CopyObject.")
	})
}

// metadataDirectiveGuard refuses a copy that names a directive S3 does not
// define.
//
// x-amz-metadata-directive takes exactly COPY or REPLACE. An unknown value used
// to fall through as COPY: the client asked for its new metadata to replace the
// source's, and got the source's metadata back with 200 - a silent difference
// no later check can notice.
func metadataDirectiveGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := strings.TrimSpace(r.Header.Get("X-Amz-Metadata-Directive"))
		if raw == "" {
			next.ServeHTTP(w, r)
			return
		}
		switch strings.ToUpper(raw) {
		case "COPY", "REPLACE":
			next.ServeHTTP(w, r)
			return
		}
		writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument",
			bucketOnlyName(r.URL.Path),
			"x-amz-metadata-directive must be COPY or REPLACE.")
	})
}

// partNumberBoundsGuard refuses part numbers outside 1..10000 with the code S3
// uses. gofakes3 answers InvalidPart for a bad part number, which clients read
// as "the part is missing" and retry the whole upload.
func partNumberBoundsGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("partNumber")
		if raw == "" {
			next.ServeHTTP(w, r)
			return
		}
		number, err := strconv.Atoi(raw)
		if err == nil && number >= 1 && number <= 10000 {
			next.ServeHTTP(w, r)
			return
		}
		writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument",
			bucketOnlyName(r.URL.Path),
			"partNumber must be an integer between 1 and 10000.")
	})
}

// trailerChecksumKey carries the checksum a framed body declared in its trailer
// from the storage layer back out to the answer.
type trailerChecksumKey struct{}

// trailerChecksum holds the whole-object checksum verified from a framed body's
// trailer. S3 stores it, echoes it on the PUT that declared it, and answers it
// on later GET/HEAD calls.
type trailerChecksum struct {
	algorithm string
	value     string
}

// recordTrailerChecksum reports the checksum a framed body's trailer declared so
// the answer can echo it. It does nothing when the request carries no holder,
// which is what every path with a plain body looks like.
func recordTrailerChecksum(ctx context.Context, algorithm, value string) {
	holder, ok := ctx.Value(trailerChecksumKey{}).(*trailerChecksum)
	if !ok || holder == nil || algorithm == "" || value == "" {
		return
	}
	holder.algorithm, holder.value = algorithm, value
}

// echoDeclaredChecksum repeats the whole-object checksum the client declared on
// a PutObject that was accepted.
//
// The value is stored and served later on GET/HEAD, but the PUT answer carried
// no header: botocore validates the header it asked to be echoed, so a client
// with checksum mode on had nothing to compare against.
func echoDeclaredChecksum(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.Header.Get("X-Amz-Copy-Source") != "" || !hasObjectKey(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := r.URL.Query()["uploadId"]; ok {
			next.ServeHTTP(w, r)
			return
		}
		algorithm, value := requestedChecksum(headerMap(r.Header))
		// The header is set before the handler runs: gofakes3 answers a PutObject
		// without an explicit WriteHeader, so a writer that waits for one never
		// gets the chance. The writer below removes it again if the request is
		// refused, so an error answer never carries the value.
		header := ""
		if algorithm != "" && value != "" {
			header = checksumHeaderPrefix + algorithm
			w.Header().Set(header, value)
		}
		// A framed body names its checksum in the trailer instead of a header;
		// the storage layer reads it while the body streams and hands it back
		// through this holder, so the answer can echo the value it verified.
		holder := &trailerChecksum{}
		writer := &checksumEchoWriter{ResponseWriter: w, header: header}
		next.ServeHTTP(writer, r.WithContext(context.WithValue(r.Context(), trailerChecksumKey{}, holder)))
		// A handler that writes no header at all leaves the status at zero and
		// net/http commits 200 once it returns; that is an accepted PUT too.
		if (writer.status == 0 || writer.status == http.StatusOK) && holder.algorithm != "" {
			w.Header().Set(checksumHeaderPrefix+holder.algorithm, holder.value)
		}
	})
}

// checksumEchoWriter keeps the eagerly-set echo header only on a 200 answer.
type checksumEchoWriter struct {
	http.ResponseWriter
	header      string
	status      int
	wroteHeader bool
}

func (w *checksumEchoWriter) WriteHeader(status int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.status = status
		if status != http.StatusOK && w.header != "" {
			w.Header().Del(w.header)
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *checksumEchoWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

// headerMap flattens request headers to the single-value form the checksum
// helpers work with.
func headerMap(headers http.Header) map[string]string {
	out := make(map[string]string, len(headers))
	for key, values := range headers {
		if len(values) > 0 {
			out[key] = values[0]
		}
	}
	return out
}

// xmlEscapeText escapes a value that is about to be placed in an XML text node.
func xmlEscapeText(s string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(s)
}
