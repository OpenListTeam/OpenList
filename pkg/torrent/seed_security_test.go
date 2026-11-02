package torrent

import (
	"strings"
	"testing"
)

// --- path traversal / malformed path handling ---------------------------------

func TestValidateSeedRejectsUnsafePaths(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"parent traversal", "../secret"},
		{"nested traversal", "a/../../secret"},
		{"absolute unix", "/etc/passwd"},
		{"empty", ""},
		{"current dir", "."},
		{"nul byte", "a\x00b"},
		{"backslash traversal", `..\secret`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := testSeed()
			seed.Files[0].Path = tc.path
			if err := ValidateSeed(seed, DefaultParseLimits()); err == nil {
				t.Fatalf("ValidateSeed() accepted unsafe path %q", tc.path)
			}
		})
	}
}

func TestValidateSeedRejectsTooManyFiles(t *testing.T) {
	seed := testSeed()
	seed.Files = make([]SeedFile, DefaultMaxSeedFiles+1)
	for i := range seed.Files {
		seed.Files[i] = SeedFile{
			Path:   "f" + strings.Repeat("0", i%3) + ".bin",
			Size:   1,
			Hashes: SeedHashes{MD5: strings.Repeat("1", 32)},
		}
	}
	if err := ValidateSeed(seed, DefaultParseLimits()); err == nil {
		t.Fatal("ValidateSeed() accepted more files than DefaultMaxSeedFiles")
	}
}

// --- sliceMd5 canonical rule ---------------------------------------------------

func TestSliceMD5FromPiecesMatchesSpec(t *testing.T) {
	fileMD5 := strings.Repeat("a", 32)

	// Zero pieces: fall back to the whole-file MD5.
	if got := SliceMD5FromPieces(nil, fileMD5); got != strings.ToUpper(fileMD5) {
		t.Fatalf("SliceMD5FromPieces(nil) = %q, want %q", got, strings.ToUpper(fileMD5))
	}
	// A single piece covers the whole file, so it equals the file MD5.
	single := strings.ToUpper(fileMD5)
	if got := SliceMD5FromPieces([]string{single}, fileMD5); got != single {
		t.Fatalf("SliceMD5FromPieces(single) = %q, want %q", got, single)
	}
	// Two or more pieces: MD5 of the newline-joined, upper-cased piece list.
	pieces := []string{strings.Repeat("b", 32), strings.Repeat("c", 32)}
	want := strings.ToUpper(GetMD5Str(strings.Join([]string{strings.Repeat("B", 32), strings.Repeat("C", 32)}, "\n")))
	if got := SliceMD5FromPieces(pieces, fileMD5); got != want {
		t.Fatalf("SliceMD5FromPieces(multi) = %q, want %q", got, want)
	}
}

// TestSliceMD5AgreesWithBuildCASInfo locks the rule shared by the hash-generation
// side and the CAS-encoding side. A divergence here silently turns rapid uploads
// into hash mismatches, so the two entry points must never drift apart.
func TestSliceMD5AgreesWithBuildCASInfo(t *testing.T) {
	fileMD5 := strings.Repeat("a", 32)
	sets := [][]string{
		nil,
		{strings.Repeat("b", 32)},
		{strings.Repeat("b", 32), strings.Repeat("c", 32)},
		{strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)},
	}
	for i, pieces := range sets {
		hw := &HashWriter{sliceMD5Hexs: pieces}
		fromWriter := hw.GetSliceMD5(fileMD5)
		fromCAS := BuildCASInfoFromMD5s(fileMD5, pieces, DefaultPieceSize).SliceMD5
		if fromWriter != fromCAS {
			t.Fatalf("case %d: GetSliceMD5() = %q, BuildCASInfoFromMD5s() = %q", i, fromWriter, fromCAS)
		}
	}
}

// --- bencode robustness --------------------------------------------------------

func TestBencodeDecodeRejectsOversizedStringLength(t *testing.T) {
	// Declares a 4GiB string while the buffer is empty; parsing must fail on the
	// declared length instead of attempting a huge allocation.
	payload := []byte("9999999999:")
	if _, err := BencodeDecode(payload); err == nil {
		t.Fatal("BencodeDecode() accepted an out-of-bounds string length")
	}
}

func TestBencodeDecodeRejectsDeepNesting(t *testing.T) {
	depth := DefaultParseLimits().MaxDepth + 2
	payload := strings.Repeat("l", depth) + strings.Repeat("e", depth)
	if _, err := BencodeDecode([]byte(payload)); err == nil {
		t.Fatal("BencodeDecode() accepted nesting beyond MaxDepth")
	}
}

func TestBencodeDecodeRejectsTrailingData(t *testing.T) {
	if _, err := BencodeDecode([]byte("i1eextra")); err == nil {
		t.Fatal("BencodeDecode() accepted trailing data")
	}
}

// --- cross-format conversion consistency --------------------------------------

// TestConvertConsistencyAcrossFormats ensures a seed survives OSS -> torrent ->
// OSS and OSS -> CAS -> OSS without losing whole-file hashes.
func TestConvertConsistencyAcrossFormats(t *testing.T) {
	original := testSeed()

	torrentData, err := EncodeSeed(original, "torrent")
	if err != nil {
		t.Fatalf("EncodeSeed(torrent) error = %v", err)
	}
	fromTorrent, err := DecodeSeed(torrentData, "torrent", DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeSeed(torrent) error = %v", err)
	}
	casData, err := EncodeCAS(fromTorrent)
	if err != nil {
		t.Fatalf("EncodeCAS() error = %v", err)
	}
	fromCAS, err := DecodeCAS(casData, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeCAS() error = %v", err)
	}
	if got := fromCAS.Files[0].Hashes.MD5; got != strings.ToUpper(original.Files[0].Hashes.MD5) {
		t.Fatalf("MD5 changed across formats: %q", got)
	}
	if len(fromCAS.Files) != len(original.Files) {
		t.Fatalf("file count changed across formats: %d", len(fromCAS.Files))
	}
}

func TestDecodeSeedRejectsUnknownFormat(t *testing.T) {
	data, err := EncodeOSS(testSeed())
	if err != nil {
		t.Fatalf("EncodeOSS() error = %v", err)
	}
	if _, err := DecodeSeed(data, "does-not-exist", DefaultParseLimits()); err == nil {
		t.Fatal("DecodeSeed() accepted an unknown format")
	}
}
