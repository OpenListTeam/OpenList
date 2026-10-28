package torrent

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"sort"
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
	if _, exists := payload["comment"]; exists {
		t.Fatal("legacy CAS with empty comment unexpectedly contains comment field")
	}
	decoded, err := DecodeCAS(encoded, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeCAS() error = %v", err)
	}
	if decoded.Comment != "" {
		t.Fatal("legacy CAS unexpectedly gained a comment")
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

func TestDecodeOSSRecomputesAvailableHashes(t *testing.T) {
	for _, global := range []bool{false, true} {
		seed := testSeed()
		seed.AvailableHashes = []string{"invented"}
		seed.AvailablePieceHashes = []string{"invented"}
		if global {
			seed.GlobalPieces = append([]string(nil), seed.Files[0].Hashes.Pieces.SHA1...)
		}
		data, err := EncodeOSS(seed)
		if err != nil {
			t.Fatal(err)
		}
		if !global && strings.Contains(string(data), "global_pieces") {
			t.Fatal("legacy OSS unexpectedly contains global_pieces")
		}
		decoded, err := DecodeOSS(data, DefaultParseLimits())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(decoded.AvailableHashes, ",") != "md5,sha1,sha256" ||
			strings.Join(decoded.AvailablePieceHashes, ",") != "md5,sha1,sha256" {
			t.Fatalf("summaries not recomputed: %v / %v", decoded.AvailableHashes, decoded.AvailablePieceHashes)
		}
	}
	seed := testSeed()
	seed.Files[0].Hashes = SeedHashes{}
	seed.AvailableHashes = []string{"md5", "sha1", "sha256"}
	seed.AvailablePieceHashes = []string{"md5", "sha1", "sha256"}
	data, err := EncodeOSS(seed)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeOSS(data, DefaultParseLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.AvailableHashes) != 0 || len(decoded.AvailablePieceHashes) != 0 {
		t.Fatal("untrusted hash summaries survived OSS decoding")
	}
}

func TestUpdateTorrentCommentPreservesOriginalInfo(t *testing.T) {
	for _, noncanonical := range []bool{false, true} {
		name := "canonical"
		if noncanonical {
			name = "noncanonical"
		}
		t.Run(name, func(t *testing.T) {
			seed := testSeed()
			seed.Comment = "old comment"
			data, err := EncodeSeed(seed, "torrent")
			if err != nil {
				t.Fatal(err)
			}
			value, err := BencodeDecode(data)
			if err != nil {
				t.Fatal(err)
			}
			root := value.(map[string]interface{})
			info := root["info"].(map[string]interface{})
			info["private"] = int64(1)
			info["future-info"] = map[string]interface{}{"binary": []byte{0, 255, 1}, "value": int64(7)}
			root["future-root"] = []interface{}{[]byte{0, 255}, int64(9)}
			extension := root[OpenListExtensionKey].(map[string]interface{})
			extension["future-extension"] = []interface{}{[]byte("keep"), int64(3)}
			data, err = BencodeEncode(root)
			if err != nil {
				t.Fatal(err)
			}
			canonicalInfo, err := BencodeEncode(info)
			if err != nil {
				t.Fatal(err)
			}
			if noncanonical {
				keys := make([]string, 0, len(info))
				for key := range info {
					keys = append(keys, key)
				}
				sort.Sort(sort.Reverse(sort.StringSlice(keys)))
				var raw bytes.Buffer
				raw.WriteByte('d')
				for _, key := range keys {
					encodedKey, err := BencodeEncode(key)
					if err != nil {
						t.Fatal(err)
					}
					encodedValue, err := BencodeEncode(info[key])
					if err != nil {
						t.Fatal(err)
					}
					if key == "private" {
						encodedValue = []byte("i01e")
					}
					raw.Write(encodedKey)
					raw.Write(encodedValue)
				}
				raw.WriteByte('e')
				if bytes.Equal(raw.Bytes(), canonicalInfo) {
					t.Fatal("fixture unexpectedly canonical")
				}
				data = bytes.Replace(data, canonicalInfo, raw.Bytes(), 1)
			}
			before, err := rawBencodeRoot(data)
			if err != nil {
				t.Fatal(err)
			}
			wantHash := sha1.Sum(before["info"])
			parsed, err := Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(parsed.InfoHash, wantHash[:]) {
				t.Fatal("Decode did not hash the original info bytes")
			}
			for _, comment := range []string{"new comment 注释", ""} {
				updated, err := UpdateSeedComment(data, ".TORRENT", comment)
				if err != nil {
					t.Fatal(err)
				}
				after, err := rawBencodeRoot(updated)
				if err != nil {
					t.Fatal(err)
				}
				for key, raw := range before {
					if key != "comment" && key != OpenListExtensionKey && !bytes.Equal(raw, after[key]) {
						t.Fatalf("updating comment changed raw field %q", key)
					}
				}
				parsed, err := Decode(updated)
				if err != nil {
					t.Fatal(err)
				}
				if parsed.Comment != comment || parsed.OpenList.Comment != comment || !bytes.Equal(parsed.InfoHash, wantHash[:]) {
					t.Fatal("updated comments or original infohash do not match")
				}
				value, err := BencodeDecode(after[OpenListExtensionKey])
				if err != nil {
					t.Fatal(err)
				}
				updatedExtension := value.(map[string]interface{})
				oldUnknown, err := BencodeEncode(extension["future-extension"])
				if err != nil {
					t.Fatal(err)
				}
				newUnknown, err := BencodeEncode(updatedExtension["future-extension"])
				if err != nil || !bytes.Equal(oldUnknown, newUnknown) {
					t.Fatal("unknown x-openlist metadata changed")
				}
			}
		})
	}
}

func TestUpdateSeedCommentContainers(t *testing.T) {
	seed := testSeed()
	data, err := EncodeOSS(seed)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateSeedComment(data, ".OSS", "updated")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := DecodeOSS(updated, DefaultParseLimits())
	if err != nil || parsed.Comment != "updated" || parsed.Files[0].Hashes.SHA256 != seed.Files[0].Hashes.SHA256 {
		t.Fatalf("OSS comment update lost metadata: %v", err)
	}
	cas, err := EncodeCAS(seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, comment := range []string{"updated", ""} {
		updated, err := UpdateSeedComment(cas, "cas", comment)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := DecodeCAS(updated, DefaultParseLimits())
		if err != nil || parsed.Comment != comment {
			t.Fatalf("CAS comment update failed: %v", err)
		}
	}
	if _, err := UpdateSeedComment(data, "unknown", "updated"); err == nil {
		t.Fatal("unsupported comment container accepted")
	}
	if _, err := UpdateSeedComment([]byte("invalid"), "torrent", "updated"); err == nil {
		t.Fatal("invalid torrent accepted")
	}
}

func TestUpdateStandardTorrentCommentDoesNotAddExtension(t *testing.T) {
	data, err := EncodeSeed(testSeed(), "torrent")
	if err != nil {
		t.Fatal(err)
	}
	value, err := BencodeDecode(data)
	if err != nil {
		t.Fatal(err)
	}
	root := value.(map[string]interface{})
	delete(root, OpenListExtensionKey)
	data, err = BencodeEncode(root)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateSeedComment(data, "torrent", "standard comment")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Decode(updated)
	if err != nil || parsed.Comment != "standard comment" || parsed.OpenList != nil {
		t.Fatalf("standard torrent extension/comment changed unexpectedly: %v", err)
	}
}

func TestCASCommentAcrossFormats(t *testing.T) {
	for _, multi := range []bool{false, true} {
		for _, format := range []string{"oss", "torrent"} {
			seed := testSeed()
			seed.Comment = "共享注释"
			if multi {
				second := seed.Files[0]
				second.Path = "second.bin"
				seed.Files[0].Size = DefaultPieceSize * 2
				seed.Files = append(seed.Files, second)
			}
			data, err := EncodeSeed(seed, format)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := DecodeSeed(data, format, DefaultParseLimits())
			if err != nil {
				t.Fatal(err)
			}
			cas, err := EncodeCAS(parsed)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := base64.StdEncoding.DecodeString(string(cas))
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range [][]byte{cas, raw} {
				fromCAS, err := DecodeCAS(input, DefaultParseLimits())
				if err != nil || fromCAS.Comment != seed.Comment || len(fromCAS.Files) != len(seed.Files) {
					t.Fatalf("%s -> CAS lost comment/files (multi=%v): %v", format, multi, err)
				}
				oss, err := EncodeOSS(fromCAS)
				if err != nil {
					t.Fatal(err)
				}
				fromOSS, err := DecodeOSS(oss, DefaultParseLimits())
				if err != nil || fromOSS.Comment != seed.Comment {
					t.Fatalf("CAS -> OSS lost comment: %v", err)
				}
			}
		}
	}
}

func TestUpdateCASCommentPreservesUnknownFieldsAndWrapper(t *testing.T) {
	seed := testSeed()
	seed.Comment = "old comment"
	encoded, err := EncodeCAS(seed)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(raw, &original); err != nil {
		t.Fatal(err)
	}
	original["future-number"] = json.RawMessage(`9007199254740993123456789`)
	original["future-metadata"] = json.RawMessage(`{"nested":["keep",true,7]}`)
	raw, err = json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, isBase64 := range []bool{false, true} {
		input := raw
		if isBase64 {
			input = []byte(base64.StdEncoding.EncodeToString(raw))
		}
		input = append(append([]byte(" \n"), input...), []byte("\n ")...)
		for _, comment := range []string{"新注释", ""} {
			updated, err := UpdateSeedComment(input, ".CAS", comment)
			if err != nil {
				t.Fatal(err)
			}
			content := updated
			if isBase64 {
				content, err = base64.StdEncoding.DecodeString(string(updated))
				if err != nil {
					t.Fatalf("base64 wrapper changed: %v", err)
				}
			} else if len(updated) == 0 || updated[0] != '{' {
				t.Fatal("plain CAS wrapper changed")
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(content, &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range original {
				if key != "comment" && !bytes.Equal(value, fields[key]) {
					t.Fatalf("CAS comment update changed field %q", key)
				}
			}
			parsed, err := DecodeCAS(updated, DefaultParseLimits())
			if err != nil || parsed.Comment != comment {
				t.Fatalf("CAS comment update failed: %v", err)
			}
			if comment == "" {
				if _, exists := fields["comment"]; exists {
					t.Fatal("cleared CAS comment field was not removed")
				}
			}
		}
	}
}
