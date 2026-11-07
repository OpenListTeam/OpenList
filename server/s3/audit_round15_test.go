package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/gofakes3"
)

// initiateUpload starts a multipart upload through the request chain and returns
// its upload id.
func initiateUpload(t *testing.T, handler http.Handler, target string) string {
	t.Helper()
	rec := openModeRequest(handler, http.MethodPost, target+"?uploads", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("initiating %s: code=%d body=%s", target, rec.Code, rec.Body.String())
	}
	var doc struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the initiate response %q: %v", rec.Body.String(), err)
	}
	if doc.UploadID == "" {
		t.Fatalf("the initiate response carries no upload id: %q", rec.Body.String())
	}
	return doc.UploadID
}

// copyPartETag returns the ETag out of an UploadPartCopy response document.
func copyPartETag(t *testing.T, body string) string {
	t.Helper()
	var doc struct {
		ETag string `xml:"ETag"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("parsing the copy part response %q: %v", body, err)
	}
	return doc.ETag
}

// TestUploadPartVerifiesTheDeclaredPayloadHash pins that a part upload whose
// x-amz-content-sha256 does not describe the bytes it carries is refused.
//
// gofakes3 folds that header into the signature but never hashes a part body,
// and it hands the storage a LimitReader sized from the declared length, so the
// streaming verifier contentSha256Guard installs never reaches EOF and never
// reports a mismatch either. A whole-object PUT is verified inside putStream,
// which counts and hashes what the driver actually received; the part path had
// no such check. A part substituted between signing and delivery was therefore
// stored, given an ETag computed from the substituted bytes, assembled at
// Complete and served back with 200 at every step - the gateway's own integrity
// header was decorative for exactly the uploads where a corrupt part is hardest
// to notice (large objects uploaded in pieces).
func TestUploadPartVerifiesTheDeclaredPayloadHash(t *testing.T) {
	handler := openModeChain(t)
	uploadID := initiateUpload(t, handler, "/mp/verified.bin")

	wrong := sha256.Sum256([]byte("the bytes the client signed"))
	rec := openModeRequest(handler, http.MethodPut,
		"/mp/verified.bin?partNumber=1&uploadId="+uploadID,
		strings.NewReader("other bytes entirely"),
		map[string]string{"X-Amz-Content-Sha256": hex.EncodeToString(wrong[:])})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a part whose declared payload hash does not match its body answered %d, want 400; body=%s",
			rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "BadDigest") {
		t.Fatalf("refusing a mismatched part must report BadDigest, got %s", rec.Body.String())
	}

	// Nothing may be stored for the rejected part: a completion naming part 1
	// has to fail with InvalidPart.
	complete := `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber>` +
		`<ETag>"00000000000000000000000000000000"</ETag></Part></CompleteMultipartUpload>`
	if rec := openModeRequest(handler, http.MethodPost, "/mp/verified.bin?uploadId="+uploadID,
		strings.NewReader(complete), nil); rec.Code == http.StatusOK {
		t.Fatalf("a part rejected for a payload hash mismatch was still assemblable: %s", rec.Body.String())
	}

	// The matching header, and the aws-chunked form that carries no whole-body
	// digest, must both keep working.
	good := sha256.Sum256([]byte("hello"))
	rec = openModeRequest(handler, http.MethodPut,
		"/mp/verified.bin?partNumber=1&uploadId="+uploadID,
		strings.NewReader("hello"),
		map[string]string{"X-Amz-Content-Sha256": hex.EncodeToString(good[:])})
	if rec.Code != http.StatusOK {
		t.Fatalf("a part with a matching payload hash answered %d: %s", rec.Code, rec.Body.String())
	}
	framed := awsChunkedBody("hello")
	rec = openModeRequest(handler, http.MethodPut,
		"/mp/verified.bin?partNumber=2&uploadId="+uploadID,
		strings.NewReader(framed),
		map[string]string{
			"X-Amz-Content-Sha256":         "STREAMING-UNSIGNED-PAYLOAD-TRAILER",
			"X-Amz-Decoded-Content-Length": "5",
			"Content-Encoding":             "aws-chunked",
			"Content-Length":               fmt.Sprint(len(framed)),
		})
	if rec.Code != http.StatusOK {
		t.Fatalf("an aws-chunked part answered %d: %s", rec.Code, rec.Body.String())
	}
}

// awsChunkedBody frames body the way an aws-chunked upload does.
func awsChunkedBody(body string) string {
	signature := strings.Repeat("a", 64)
	var builder strings.Builder
	for len(body) > 0 {
		size := len(body)
		if size > 2 {
			size = 2
		}
		fmt.Fprintf(&builder, "%x;chunk-signature=%s\r\n%s\r\n", size, signature, body[:size])
		body = body[size:]
	}
	fmt.Fprintf(&builder, "0;chunk-signature=%s\r\n\r\n", signature)
	return builder.String()
}

// listPartsDocument is the part of a ListParts response this package asserts on.
type listPartsDocument struct {
	XMLName      xml.Name `xml:"ListPartsResult"`
	Bucket       string   `xml:"Bucket"`
	Key          string   `xml:"Key"`
	UploadID     string   `xml:"UploadId"`
	StorageClass string   `xml:"StorageClass"`
	MaxParts     int      `xml:"MaxParts"`
	IsTruncated  bool     `xml:"IsTruncated"`
	Parts        []struct {
		PartNumber int    `xml:"PartNumber"`
		ETag       string `xml:"ETag"`
		Size       int64  `xml:"Size"`
	} `xml:"Part"`
}

func listParts(t *testing.T, handler http.Handler, target, uploadID string) listPartsDocument {
	t.Helper()
	rec := openModeRequest(handler, http.MethodGet, target+"?uploadId="+uploadID, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("ListParts %s: code=%d body=%s", target, rec.Code, rec.Body.String())
	}
	var doc listPartsDocument
	if err := xml.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("parsing the ListParts response %q: %v", rec.Body.String(), err)
	}
	return doc
}

// TestListPartsReportsCopiedParts pins that ListParts describes every part of an
// upload, including the ones written by UploadPartCopy.
//
// gofakes3 keeps its own list of the parts of an upload and only adds to it when
// its own part-upload handler runs. UploadPartCopy is implemented in front of it
// and writes the part straight into the backend, so the copies exist - Complete
// validates and assembles them - while ListParts answers the tracked subset
// only. A client that resumes an interrupted server-side copy by asking which
// parts already exist, or that verifies a completed upload by summing the part
// sizes, is told the upload is empty; s3cmd/SDK "upload again" logic then
// re-copies parts that are already there, and any tool that sizes an upload from
// ListParts before completing it reads a total that does not include the copies.
func TestListPartsReportsCopiedParts(t *testing.T) {
	handler := openModeChain(t)
	if rec := openModeRequest(handler, http.MethodPut, "/mp/source.bin",
		strings.NewReader("0123456789"), nil); rec.Code != http.StatusOK {
		t.Fatalf("seeding the copy source: %d %s", rec.Code, rec.Body.String())
	}
	uploadID := initiateUpload(t, handler, "/mp/assembled.bin")

	// Part 1 is a server-side copy of source[2:6], part 2 an ordinary upload.
	copied := openModeRequest(handler, http.MethodPut,
		"/mp/assembled.bin?partNumber=1&uploadId="+uploadID, nil,
		map[string]string{
			"X-Amz-Copy-Source":       "/mp/source.bin",
			"X-Amz-Copy-Source-Range": "bytes=2-5",
		})
	if copied.Code != http.StatusOK {
		t.Fatalf("copying part 1: code=%d body=%s", copied.Code, copied.Body.String())
	}
	copiedETag := copyPartETag(t, copied.Body.String())
	plain := openModeRequest(handler, http.MethodPut,
		"/mp/assembled.bin?partNumber=2&uploadId="+uploadID,
		strings.NewReader("abcde"), nil)
	if plain.Code != http.StatusOK {
		t.Fatalf("uploading part 2: %d %s", plain.Code, plain.Body.String())
	}
	plainETag := plain.Header().Get("ETag")

	doc := listParts(t, handler, "/mp/assembled.bin", uploadID)
	if len(doc.Parts) != 2 {
		t.Fatalf("ListParts reports %d part(s) of an upload that holds 2; body parts=%+v",
			len(doc.Parts), doc.Parts)
	}
	if doc.Parts[0].PartNumber != 1 || doc.Parts[1].PartNumber != 2 {
		t.Fatalf("ListParts reported parts %d,%d, want 1,2",
			doc.Parts[0].PartNumber, doc.Parts[1].PartNumber)
	}
	if doc.Parts[0].Size != 4 {
		t.Fatalf("copied part 1 is reported as %d bytes, want the 4 bytes of bytes=2-5", doc.Parts[0].Size)
	}
	if doc.Parts[1].Size != 5 {
		t.Fatalf("part 2 is reported as %d bytes, want 5", doc.Parts[1].Size)
	}
	if doc.Parts[0].ETag != copiedETag {
		t.Fatalf("copied part 1 ETag %q does not match the one UploadPartCopy returned %q",
			doc.Parts[0].ETag, copiedETag)
	}
	if doc.Parts[1].ETag != plainETag {
		t.Fatalf("part 2 ETag %q does not match the one the upload returned %q",
			doc.Parts[1].ETag, plainETag)
	}

	complete := fmt.Sprintf("<CompleteMultipartUpload>"+
		"<Part><PartNumber>1</PartNumber><ETag>%s</ETag></Part>"+
		"<Part><PartNumber>2</PartNumber><ETag>%s</ETag></Part>"+
		"</CompleteMultipartUpload>", copiedETag, plainETag)
	if rec := openModeRequest(handler, http.MethodPost, "/mp/assembled.bin?uploadId="+uploadID,
		strings.NewReader(complete), nil); rec.Code != http.StatusOK {
		t.Fatalf("completing the upload: code=%d body=%s", rec.Code, rec.Body.String())
	}
	got := openModeRequest(handler, http.MethodGet, "/mp/assembled.bin", nil, nil)
	if body := got.Body.String(); body != "2345abcde" {
		t.Fatalf("the assembled object is %q, want %q", body, "2345abcde")
	}
}

// TestAbortDuringPartUploadEndsTheWriterCleanly pins what an UploadPart in
// flight is told when the upload is aborted underneath it.
//
// Abort removes the upload's temp directory immediately, and the part that is
// still streaming is renamed into it when it finishes: the rename fails on a
// directory that no longer exists and the client is answered 500 InternalError.
// The upload does not exist at that point, so the honest answer is
// NoSuchUpload; a 500 tells the SDK the server broke and makes it retry the part
// with backoff instead of reporting that the upload is gone.
func TestAbortDuringPartUploadEndsTheWriterCleanly(t *testing.T) {
	ctx := context.Background()
	backend, _ := setupMultipartBackend(t)

	uploadID, err := backend.CreateMultipartUpload(ctx, "mp", "aborted.bin", nil)
	if err != nil {
		t.Fatalf("CreateMultipartUpload: %v", err)
	}
	value, ok := backend.uploads.Load(uploadID)
	if !ok {
		t.Fatalf("the upload was not registered")
	}
	state := value.(*multipartState)
	dir := state.dir

	reader, writer := io.Pipe()
	var wg sync.WaitGroup
	var uploadErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, uploadErr = backend.UploadPart(ctx, "mp", "aborted.bin", uploadID, 1, 10, reader)
	}()

	// Let the part get into the streaming state before aborting.
	deadline := time.Now().Add(2 * time.Second)
	for {
		state.mu.Lock()
		inflight := state.inflight
		state.mu.Unlock()
		if inflight > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the part never started streaming")
		}
		time.Sleep(time.Millisecond)
	}

	if err := backend.AbortMultipartUpload(ctx, "mp", "aborted.bin", uploadID); err != nil {
		t.Fatalf("AbortMultipartUpload: %v", err)
	}
	// The writer finishes its body after the abort, exactly like a client whose
	// request was already on the wire.
	if _, err := writer.Write([]byte("0123456789")); err != nil {
		t.Fatalf("writing the part body: %v", err)
	}
	_ = writer.Close()
	wg.Wait()

	if s3ErrorCode(uploadErr) != gofakes3.ErrNoSuchUpload {
		t.Fatalf("a part in flight when its upload was aborted returned %v (code %q), want NoSuchUpload",
			uploadErr, s3ErrorCode(uploadErr))
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("the aborted upload's directory %s was left behind (stat err %v)", dir, statErr)
	}
}

// TestListPartsPagesAndReportsUnknownUploads pins the shape of the part listing
// the gateway answers from its own upload state: paging by max-parts and
// part-number-marker, an upload that has no parts yet, and an upload id nobody
// knows (which must stay NoSuchUpload rather than becoming an empty listing).
func TestListPartsPagesAndReportsUnknownUploads(t *testing.T) {
	handler := openModeChain(t)
	uploadID := initiateUpload(t, handler, "/mp/paged.bin")

	if doc := listParts(t, handler, "/mp/paged.bin", uploadID); len(doc.Parts) != 0 {
		t.Fatalf("a fresh upload listed %d part(s)", len(doc.Parts))
	}
	for partNumber := 1; partNumber <= 3; partNumber++ {
		rec := openModeRequest(handler, http.MethodPut,
			fmt.Sprintf("/mp/paged.bin?partNumber=%d&uploadId=%s", partNumber, uploadID),
			strings.NewReader(fmt.Sprint(partNumber)), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("uploading part %d: %d %s", partNumber, rec.Code, rec.Body.String())
		}
	}

	first := openModeRequest(handler, http.MethodGet,
		"/mp/paged.bin?uploadId="+uploadID+"&max-parts=2", nil, nil)
	var firstDoc listPartsDocument
	if err := xml.Unmarshal(first.Body.Bytes(), &firstDoc); err != nil {
		t.Fatalf("parsing the paged listing %q: %v", first.Body.String(), err)
	}
	if len(firstDoc.Parts) != 2 || firstDoc.Parts[0].PartNumber != 1 || firstDoc.Parts[1].PartNumber != 2 {
		t.Fatalf("max-parts=2 returned %+v, want parts 1 and 2", firstDoc.Parts)
	}
	if !firstDoc.IsTruncated {
		t.Fatalf("max-parts=2 over three parts must be truncated: %s", first.Body.String())
	}

	second := openModeRequest(handler, http.MethodGet,
		"/mp/paged.bin?uploadId="+uploadID+"&part-number-marker=2", nil, nil)
	var secondDoc listPartsDocument
	if err := xml.Unmarshal(second.Body.Bytes(), &secondDoc); err != nil {
		t.Fatalf("parsing the second page %q: %v", second.Body.String(), err)
	}
	if len(secondDoc.Parts) != 1 || secondDoc.Parts[0].PartNumber != 3 {
		t.Fatalf("part-number-marker=2 returned %+v, want only part 3", secondDoc.Parts)
	}
	if secondDoc.IsTruncated {
		t.Fatalf("the last page must not be truncated: %s", second.Body.String())
	}

	unknown := openModeRequest(handler, http.MethodGet,
		"/mp/paged.bin?uploadId=00000000000000000000000000000000", nil, nil)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("listing an unknown upload answered %d, want 404: %s", unknown.Code, unknown.Body.String())
	}
	if !strings.Contains(unknown.Body.String(), "NoSuchUpload") {
		t.Fatalf("listing an unknown upload must say NoSuchUpload, got %s", unknown.Body.String())
	}
}
