package s3

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash"
	"hash/crc32"
	"strings"
)

// etagMetadataKey is where the gateway keeps the ETag it computed for an object.
// It is not user metadata: the key never goes out as a header, it only feeds the
// ETag of GET, HEAD, listing entries and copies.
const etagMetadataKey = "X-Openlist-S3-Etag"

// checksumHeaderPrefix names the request headers that carry a whole-object
// checksum. AWS verifies them and refuses a body that does not match, which is
// the only thing protecting a client that sends its own integrity value.
const checksumHeaderPrefix = "x-amz-checksum-"

// newChecksumHash returns the hash for one of the x-amz-checksum-* algorithms
// this gateway can verify, or nil for anything else (crc64nvme is not offered:
// Go's hash/crc64 wants a reversed polynomial, and a wrong table would reject
// valid uploads instead of protecting them).
func newChecksumHash(algorithm string) hash.Hash {
	switch algorithm {
	case "crc32":
		return crc32.NewIEEE()
	case "crc32c":
		return crc32.New(crc32.MakeTable(crc32.Castagnoli))
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	}
	return nil
}

// requestedChecksum returns the algorithm and the base64 value a request
// declares for the whole object, if it declares one this gateway can verify.
func requestedChecksum(meta map[string]string) (string, string) {
	for key, value := range meta {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, checksumHeaderPrefix) {
			continue
		}
		algorithm := strings.TrimPrefix(lower, checksumHeaderPrefix)
		if newChecksumHash(algorithm) == nil {
			continue
		}
		return algorithm, strings.TrimSpace(value)
	}
	return "", ""
}

// checksumMatches reports whether the digest of what was stored equals the value
// the client declared.
func checksumMatches(algorithm string, sum []byte, declared string) bool {
	if declared == "" {
		return true
	}
	if base64.StdEncoding.EncodeToString(sum) == declared {
		return true
	}
	// Some clients send the hex form; accept either spelling of the same value.
	return strings.EqualFold(hex.EncodeToString(sum), declared)
}

// dropUnverifiableChecksums removes x-amz-checksum-* headers the gateway did not
// verify: replaying a value nobody checked is a claim the object may not honour.
func dropUnverifiableChecksums(meta map[string]string, verified string) {
	for key := range meta {
		lower := strings.ToLower(key)
		if !strings.HasPrefix(lower, checksumHeaderPrefix) {
			continue
		}
		if strings.TrimPrefix(lower, checksumHeaderPrefix) != verified {
			delete(meta, key)
		}
	}
}
