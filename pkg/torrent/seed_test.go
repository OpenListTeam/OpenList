package torrent

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func testSeed() *Seed {
	return &Seed{
		Format:    OSSFormat,
		Version:   OSSVersion,
		Name:      "example.bin",
		CreatedAt: time.Unix(1, 0).UTC().Format(time.RFC3339),
		CreatedBy: "OpenList",
		PieceSize: DefaultPieceSize,
		Files: []SeedFile{{
			Path: "example.bin",
			Size: DefaultPieceSize + 1,
			Hashes: SeedHashes{
				MD5:    strings.Repeat("1", 32),
				SHA1:   strings.Repeat("2", 40),
				SHA256: strings.Repeat("3", 64),
				Pieces: &SeedPieceHashes{
					MD5:    []string{strings.Repeat("4", 32), strings.Repeat("5", 32)},
					SHA1:   []string{strings.Repeat("6", 40), strings.Repeat("7", 40)},
					SHA256: []string{strings.Repeat("8", 64), strings.Repeat("9", 64)},
				},
			},
		}},
	}
}

func TestOSSRoundTrip(t *testing.T) {
	encoded, err := EncodeOSS(testSeed())
	if err != nil {
		t.Fatalf("EncodeOSS() error = %v", err)
	}
	decoded, err := DecodeOSS(encoded, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeOSS() error = %v", err)
	}
	if decoded.Name != "example.bin" || len(decoded.Files) != 1 || decoded.Files[0].Hashes.SHA256 == "" {
		t.Fatalf("DecodeOSS() returned incomplete seed: %#v", decoded)
	}
}

func TestCASWireFormatIsLegacyCompatible(t *testing.T) {
	encoded, err := EncodeCAS(testSeed())
	if err != nil {
		t.Fatalf("EncodeCAS() error = %v", err)
	}
	decodedJSON, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatalf("base64.DecodeString() error = %v", err)
	}
	var payload map[string]any
	if err = json.Unmarshal(decodedJSON, &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if len(payload) != 5 {
		t.Fatalf("CAS field count = %d, want 5: %#v", len(payload), payload)
	}
	for _, key := range []string{"name", "size", "md5", "sliceMd5", "create_time"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("CAS payload missing %q: %#v", key, payload)
		}
	}
	decoded, err := DecodeCAS(encoded, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeCAS() error = %v", err)
	}
	if decoded.Files[0].Hashes.MD5 != strings.Repeat("1", 32) {
		t.Fatalf("DecodeCAS() MD5 = %q", decoded.Files[0].Hashes.MD5)
	}
}

func TestTorrentRoundTripPreservesOpenListExtension(t *testing.T) {
	encoded, err := EncodeSeed(testSeed(), "torrent")
	if err != nil {
		t.Fatalf("EncodeSeed(torrent) error = %v", err)
	}
	decoded, err := DecodeSeed(encoded, "torrent", DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeSeed(torrent) error = %v", err)
	}
	if decoded.Files[0].Hashes.SHA256 != strings.Repeat("3", 64) {
		t.Fatalf("torrent extension lost SHA-256: %#v", decoded.Files[0].Hashes)
	}
}

func TestValidateSeedRejectsTraversalAndInvalidHash(t *testing.T) {
	seed := testSeed()
	seed.Files[0].Path = "../secret"
	if err := ValidateSeed(seed, DefaultParseLimits()); err == nil {
		t.Fatal("ValidateSeed() accepted path traversal")
	}
	seed = testSeed()
	seed.Files[0].Hashes.MD5 = "not-a-hash"
	if err := ValidateSeed(seed, DefaultParseLimits()); err == nil {
		t.Fatal("ValidateSeed() accepted an invalid hash")
	}
}

func TestDecodeSeedHonorsSizeLimit(t *testing.T) {
	data := []byte(`{"format":"openlist-sharing-seed"}`)
	limits := DefaultParseLimits()
	limits.MaxBytes = int64(len(data) - 1)
	if _, err := DecodeSeed(data, "oss", limits); err == nil {
		t.Fatal("DecodeSeed() accepted input above MaxBytes")
	}
}
