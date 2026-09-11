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
	// The five legacy fields must always be present so the reference client can
	// parse the payload. slice_md5s / slice_size are optional extensions.
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
	// Per-piece MD5 list must round-trip through the slice_md5s extension.
	if decoded.Files[0].Hashes.Pieces == nil || len(decoded.Files[0].Hashes.Pieces.MD5) != 2 {
		t.Fatalf("DecodeCAS() piece MD5 list = %#v", decoded.Files[0].Hashes.Pieces)
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

func TestCASCloudGeneralizationRoundTrip(t *testing.T) {
	seed := testSeed()
	seed.Files[0].CASSliceMD5 = strings.Repeat("a", 32)
	seed.Files[0].CASCloud = CloudAliyundriveOpen

	// CAS (base64 JSON) round-trip must preserve the cloud identifier.
	encoded, err := EncodeCAS(seed)
	if err != nil {
		t.Fatalf("EncodeCAS() error = %v", err)
	}
	decoded, err := DecodeCAS(encoded, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeCAS() error = %v", err)
	}
	if got := decoded.Files[0].CASCloud; got != CloudAliyundriveOpen {
		t.Fatalf("DecodeCAS() cloud = %q, want %q", got, CloudAliyundriveOpen)
	}

	// Torrent bencode round-trip must preserve the cloud identifier too.
	torrentData, err := EncodeSeed(seed, "torrent")
	if err != nil {
		t.Fatalf("EncodeSeed(torrent) error = %v", err)
	}
	decodedTorrent, err := DecodeSeed(torrentData, "torrent", DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeSeed(torrent) error = %v", err)
	}
	if got := decodedTorrent.Files[0].CASCloud; got != CloudAliyundriveOpen {
		t.Fatalf("DecodeSeed(torrent) cloud = %q, want %q", got, CloudAliyundriveOpen)
	}
}

func TestBuildCASInfoFromMD5sDefaultsToCloud189(t *testing.T) {
	info := BuildCASInfoFromMD5s(strings.Repeat("1", 32), []string{strings.Repeat("4", 32)}, DefaultPieceSize)
	if info.Cloud != Cloud189 {
		t.Fatalf("BuildCASInfoFromMD5s() cloud = %q, want %q", info.Cloud, Cloud189)
	}
	other := BuildCASInfoFromMD5sWithCloud(strings.Repeat("1", 32), []string{strings.Repeat("4", 32)}, DefaultPieceSize, Cloud115)
	if other.Cloud != Cloud115 {
		t.Fatalf("BuildCASInfoFromMD5sWithCloud() cloud = %q, want %q", other.Cloud, Cloud115)
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
