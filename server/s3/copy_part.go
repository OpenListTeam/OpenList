package s3

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/gofakes3"
	log "github.com/sirupsen/logrus"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

// uploadPartCopyResult is the document S3 answers an UploadPartCopy with.
type uploadPartCopyResult struct {
	XMLName      xml.Name `xml:"CopyPartResult"`
	Xmlns        string   `xml:"xmlns,attr"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag"`
}

// uploadPartCopyHandler implements UploadPartCopy: a part written from an
// existing object instead of from the request body.
//
// gofakes3 has no such operation (v0.8.2 only recognises X-Amz-Copy-Source for
// whole-object copies), so a "PUT ?partNumber=N&uploadId=U" carrying that header
// fell into the ordinary part upload and stored an empty part, answering 200
// with no CopyPartResult. A client doing a server-side copy of a large object -
// aws s3 cp, s3manager, rclone - then completed an upload full of zero-byte
// parts, or failed with InvalidPart without ever learning why.
//
// The destination bucket and the copy source are already authorized by
// bucketAuthHandler, which validates X-Amz-Copy-Source against the caller's rule
// before anything reaches here; this handler only performs the copy.
func uploadPartCopyHandler(next http.Handler, backend *s3Backend, keys *keyRing) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			next.ServeHTTP(w, r)
			return
		}
		query := r.URL.Query()
		partNumbers, hasPart := query["partNumber"]
		uploadIDs, hasUpload := query["uploadId"]
		if !hasPart || !hasUpload || strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source")) == "" {
			next.ServeHTTP(w, r)
			return
		}

		// This handler runs in front of gofakes3's authentication middleware and
		// talks to the backend directly, so nothing has verified the signature
		// yet: bucketAuthHandler only matches the access key id against a rule.
		// Without this check any caller who knew an access key id could copy
		// objects with a forged signature. Hand the request to gofakes3 when the
		// signature does not verify, so the client gets its standard
		// SignatureDoesNotMatch/InvalidAccessKeyId answer.
		if !s3RequestAuthorized(r, keys) {
			next.ServeHTTP(w, r)
			return
		}

		bucket, object, isObjectPath := parseObjectPath(r.URL.Path)
		if !isObjectPath || bucket == "" || object == "" {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
				"UploadPartCopy needs a destination bucket and key")
			return
		}

		partNumber, err := strconv.Atoi(strings.TrimSpace(partNumbers[0]))
		if err != nil || partNumber <= 0 || partNumber > gofakes3.MaxUploadPartNumber {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
				fmt.Sprintf("partNumber must be an integer between 1 and %d", gofakes3.MaxUploadPartNumber))
			return
		}
		uploadID := gofakes3.UploadID(strings.TrimSpace(uploadIDs[0]))

		sourceBucket, sourceKey, present := copySourceRef(r)
		if !present || sourceBucket == "" || sourceKey == "" {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket,
				"X-Amz-Copy-Source must name a bucket and a key")
			return
		}

		rangeRequest, err := copySourceRangeRequest(r)
		if err != nil {
			writeS3Error(w, r, http.StatusBadRequest, "InvalidArgument", bucket, err.Error())
			return
		}

		source, err := backend.GetObject(r.Context(), sourceBucket, sourceKey, rangeRequest)
		if err != nil && isRangeError(err) {
			// The source exists but the requested slice of it does not. Answering
			// NoSuchKey hid a bad X-Amz-Copy-Source-Range behind a wrong claim
			// about the source object.
			writeS3Error(w, r, http.StatusRequestedRangeNotSatisfiable, "InvalidRange", sourceBucket,
				"The X-Amz-Copy-Source-Range you specified is not satisfiable for the source object.")
			return
		}
		if err != nil {
			// A missing source is the client's mistake, not a gateway failure.
			log.Warnf("s3 multipart: copy part source %s/%s unreadable: %v", sourceBucket, sourceKey, err)
			writeS3Error(w, r, http.StatusNotFound, "NoSuchKey", sourceBucket,
				fmt.Sprintf("The copy source %s/%s does not exist or is not readable.", sourceBucket, sourceKey))
			return
		}
		defer source.Contents.Close()

		length := source.Size
		if source.Range != nil {
			length = source.Range.Length
		}

		etag, err := backend.UploadPart(r.Context(), bucket, object, uploadID, partNumber, length, source.Contents)
		if err != nil {
			status, code, message := copyPartError(err)
			if status == http.StatusInternalServerError {
				log.Errorf("s3 multipart: copy part %d of %s failed: %v", partNumber, uploadID, err)
			}
			writeS3Error(w, r, status, code, bucket, message)
			return
		}

		payload, err := xml.MarshalIndent(uploadPartCopyResult{
			Xmlns:        "http://s3.amazonaws.com/doc/2006-03-01/",
			LastModified: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
			ETag:         etag,
		}, "", "  ")
		if err != nil {
			writeS3Error(w, r, http.StatusInternalServerError, "InternalError", bucket,
				"The copy result could not be encoded.")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(xml.Header))
		_, _ = w.Write(payload)
	})
}

// copyPartError names the failure of a part copy the way S3 does. Reporting a
// mapping failure keeps a client error (unknown upload, bad part number) from
// looking like a gateway outage, which the SDKs would retry.
// isRangeError reports whether the failure came from the storage layer refusing
// the requested slice of the source object.
func isRangeError(err error) bool {
	return errors.Is(err, gofakes3.ErrInvalidRange) || errors.Is(err, http_range.ErrNoOverlap)
}

func copyPartError(err error) (int, string, string) {
	var response *gofakes3.ErrorResponse
	if errors.As(err, &response) {
		switch response.Code {
		case gofakes3.ErrNoSuchUpload:
			return http.StatusNotFound, string(response.Code), "The specified multipart upload does not exist."
		case gofakes3.ErrIncompleteBody:
			return http.StatusBadRequest, string(response.Code), "The copy source ended before the part was complete."
		default:
			return http.StatusBadRequest, string(response.Code), response.Message
		}
	}
	switch {
	case errors.Is(err, gofakes3.ErrNoSuchUpload):
		return http.StatusNotFound, string(gofakes3.ErrNoSuchUpload), "The specified multipart upload does not exist."
	case errors.Is(err, gofakes3.ErrInvalidPart):
		return http.StatusBadRequest, string(gofakes3.ErrInvalidPart), "The part number is not valid."
	case errors.Is(err, gofakes3.ErrIncompleteBody):
		return http.StatusBadRequest, string(gofakes3.ErrIncompleteBody), "The copy source ended before the part was complete."
	}
	return http.StatusInternalServerError, "InternalError", "The part could not be stored."
}

// copySourceRangeRequest turns X-Amz-Copy-Source-Range into the range the
// storage layer understands. Only the single "bytes=start-end" form S3 accepts
// is honoured; anything else is a client error.
func copySourceRangeRequest(r *http.Request) (*gofakes3.ObjectRangeRequest, error) {
	raw := strings.TrimSpace(r.Header.Get("X-Amz-Copy-Source-Range"))
	if raw == "" {
		return nil, nil
	}
	spec := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(raw), "bytes="))
	first, last, found := strings.Cut(spec, "-")
	if !found || first == "" || last == "" {
		return nil, fmt.Errorf("X-Amz-Copy-Source-Range must be of the form bytes=start-end")
	}
	start, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil || start < 0 {
		return nil, fmt.Errorf("X-Amz-Copy-Source-Range has an invalid start offset")
	}
	end, err := strconv.ParseInt(strings.TrimSpace(last), 10, 64)
	if err != nil || end < start {
		return nil, fmt.Errorf("X-Amz-Copy-Source-Range has an invalid end offset")
	}
	return &gofakes3.ObjectRangeRequest{Start: start, End: end}, nil
}
