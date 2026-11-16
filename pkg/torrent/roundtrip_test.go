package torrent

import (
	"bytes"
	"testing"
)

// TestTorrentRoundTripCrossingPieces 回归：多文件 torrent 的 piece 跨文件时，
// 「解析 → 重新编码」不能失败，pieces 必须逐字节一致。
// 之前 SeedFromTorrent 只在无 x-openlist 时保留整包 pieces，带 x-openlist 的
// 种子重新编码会按文件拼接 pieces，被判成 piece boundary crosses the next file。
func TestTorrentRoundTripCrossingPieces(t *testing.T) {
	const piece = int64(1 << 20)
	total := (piece + 123) + (piece + 456)
	count := int((total + piece - 1) / piece)
	pieces := make([]byte, 0, count*20)
	for i := 0; i < count; i++ {
		for j := 0; j < 20; j++ {
			pieces = append(pieces, byte(i*20+j))
		}
	}
	src := &Torrent{Info: TorrentInfo{
		Name:        "crossing",
		PieceLength: piece,
		Pieces:      pieces,
		Files: []TorrentFile{
			{Length: piece + 123, Path: []string{"a.pdf"}},
			{Length: piece + 456, Path: []string{"b.pdf"}},
		},
	}}
	encoded, err := src.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	seed, err := SeedFromTorrent(decoded, DefaultParseLimits())
	if err != nil {
		t.Fatalf("SeedFromTorrent() error = %v", err)
	}
	if len(seed.GlobalPieces) != count {
		t.Fatalf("GlobalPieces = %d, want %d", len(seed.GlobalPieces), count)
	}
	// 跨文件时 pieces 无法拆到单个文件，必须在种子级标出「已带 sha1 pieces」，
	// 否则前端会把已有哈希全显示成 needs file
	found := false
	for _, algo := range seed.AvailablePieceHashes {
		if algo == "sha1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("AvailablePieceHashes = %v, want sha1", seed.AvailablePieceHashes)
	}
	if diags := DiagnoseConversion(seed, "torrent"); len(diags) > 0 {
		t.Fatalf("torrent seed should be re-encodable, got %v", diags)
	}
	rebuilt, diags := TorrentFromSeed(seed)
	if len(diags) > 0 {
		t.Fatalf("TorrentFromSeed() diagnostics = %v", diags)
	}
	if !bytes.Equal(rebuilt.Info.Pieces, pieces) {
		t.Fatal("rebuilt pieces differ from the original torrent")
	}
	ossData, err := EncodeOSS(seed)
	if err != nil {
		t.Fatalf("EncodeOSS() error = %v", err)
	}
	fromOSS, err := DecodeOSS(ossData, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeOSS() error = %v", err)
	}
	rebuilt, diags = TorrentFromSeed(fromOSS)
	if len(diags) > 0 {
		t.Fatalf("TorrentFromSeed(OSS) diagnostics = %v", diags)
	}
	if !bytes.Equal(rebuilt.Info.Pieces, pieces) {
		t.Fatal("torrent -> OSS -> torrent changed crossing pieces")
	}
	if len(fromOSS.AvailablePieceHashes) != 1 || fromOSS.AvailablePieceHashes[0] != "sha1" {
		t.Fatalf("OSS available pieces = %v, want only sha1", fromOSS.AvailablePieceHashes)
	}
}

// TestTorrentFromSeedStillRejectsPerFilePieces 确认没有整包 pieces 时，
// 逐文件 pieces 仍然不能直接拼成跨文件的 torrent（避免生成损坏种子）。
func TestTorrentFromSeedStillRejectsPerFilePieces(t *testing.T) {
	const piece = int64(1 << 20)
	seed := NewSeed("crossing", "OpenList", piece)
	seed.Files = []SeedFile{
		{Path: "a.pdf", Size: piece + 123, Hashes: SeedHashes{Pieces: &SeedPieceHashes{SHA1: []string{
			"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		}}}},
		{Path: "b.pdf", Size: piece + 456, Hashes: SeedHashes{Pieces: &SeedPieceHashes{SHA1: []string{
			"cccccccccccccccccccccccccccccccccccccccc",
			"dddddddddddddddddddddddddddddddddddddddd",
		}}}},
	}
	if diags := DiagnoseConversion(seed, "torrent"); len(diags) == 0 {
		t.Fatal("per-file pieces with crossing boundaries should not be convertible")
	}
}

func TestSeedFromTorrentUsesByteOffsetsForAlignedFiles(t *testing.T) {
	const piece = int64(16 * 1024)
	pieces := make([]byte, 3*20)
	for i := range pieces {
		pieces[i] = byte(i)
	}
	src := &Torrent{Info: TorrentInfo{
		Name: "crossing", PieceLength: piece, Pieces: pieces,
		Files: []TorrentFile{
			{Path: []string{"a.bin"}, Length: piece / 2},
			{Path: []string{"b.bin"}, Length: piece / 2},
			{Path: []string{"c.bin"}, Length: piece + 1},
		},
	}}
	seed, err := SeedFromTorrent(src, DefaultParseLimits())
	if err != nil {
		t.Fatal(err)
	}
	if seed.Files[0].Hashes.Pieces != nil || seed.Files[1].Hashes.Pieces != nil {
		t.Fatal("crossing file pieces were falsely treated as independent pieces")
	}
	last := seed.Files[2].Hashes.Pieces
	if last == nil || len(last.SHA1) != 2 || last.SHA1[0] != seed.GlobalPieces[1] || last.SHA1[1] != seed.GlobalPieces[2] {
		t.Fatalf("aligned final file has incorrect pieces: %#v", last)
	}
}

func TestTorrentOSSConversionWithOpenListCrossingPieces(t *testing.T) {
	const piece = int64(16 * 1024)
	seed := NewSeed("crossing", "OpenList", piece)
	seed.Files = []SeedFile{{Path: "a.bin", Size: piece / 2}, {Path: "b.bin", Size: piece / 2}}
	seed.GlobalPieces = []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	original, err := EncodeSeed(seed, "torrent")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := DecodeSeed(original, "torrent", DefaultParseLimits())
	if err != nil {
		t.Fatal(err)
	}
	oss, err := EncodeOSS(parsed)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = DecodeOSS(oss, DefaultParseLimits())
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := EncodeSeed(parsed, "torrent")
	if err != nil {
		t.Fatal(err)
	}
	before, err := Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Decode(rebuilt)
	if err != nil {
		t.Fatal(err)
	}
	if before.GetInfoHashHex() != after.GetInfoHashHex() || !bytes.Equal(before.Info.Pieces, after.Info.Pieces) {
		t.Fatal("torrent with x-openlist changed infohash or pieces across OSS")
	}
}
