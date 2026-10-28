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
