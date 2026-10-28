package torrent

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// TestSingleFileTorrentNameMismatchParses 回归：单文件 torrent 的 info.name 是
// 文件名（带扩展名），而 x-openlist 里的 seed.name 可以不带扩展名。
// 之前一致性校验直接比较两者，判定种子损坏，导致种子解析失败、前端只能走
// 本地兜底解析（没有 conversions / 没有哈希，全部显示 needs file）。
func TestSingleFileTorrentNameMismatchParses(t *testing.T) {
	const piece = int64(1 << 20)
	seed := NewSeed("book-title", "OpenList", piece)
	seed.CreatedAt = time.Unix(1, 0).UTC().Format(time.RFC3339)
	seed.Files = []SeedFile{{
		Path: "book-title.pdf",
		Size: piece,
		Hashes: SeedHashes{
			MD5:    strings.Repeat("1", 32),
			SHA1:   strings.Repeat("2", 40),
			SHA256: strings.Repeat("3", 64),
			Pieces: &SeedPieceHashes{SHA1: []string{strings.Repeat("a", 40)}},
		},
	}}
	// 整包 pieces 必须与 x-openlist 里记录的逐文件 pieces 一致
	pieces, err := hex.DecodeString(strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("decode piece: %v", err)
	}
	src := &Torrent{
		Info: TorrentInfo{
			Name:        "book-title.pdf", // 文件名，与 seed.Name 不同
			Length:      piece,
			PieceLength: piece,
			Pieces:      pieces,
			MD5Sum:      seed.Files[0].Hashes.MD5,
		},
		OpenList: seed,
	}
	encoded, err := src.Encode()
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	parsed, err := SeedFromTorrent(decoded, DefaultParseLimits())
	if err != nil {
		t.Fatalf("SeedFromTorrent() error = %v", err)
	}
	if len(parsed.Files) != 1 || parsed.Files[0].Hashes.SHA256 == "" {
		t.Fatalf("parsed seed lost hashes: %+v", parsed.Files)
	}
	rebuilt, diags := TorrentFromSeed(parsed)
	if len(diags) > 0 {
		t.Fatalf("TorrentFromSeed() diagnostics = %v", diags)
	}
	if rebuilt.Info.Name != src.Info.Name {
		t.Fatalf("rebuilt info.name = %q, want %q", rebuilt.Info.Name, src.Info.Name)
	}
	rebuiltData, err := rebuilt.Encode()
	if err != nil {
		t.Fatalf("rebuilt.Encode() error = %v", err)
	}
	reparsed, err := Decode(rebuiltData)
	if err != nil {
		t.Fatalf("Decode(rebuilt) error = %v", err)
	}
	if reparsed.GetInfoHashHex() != decoded.GetInfoHashHex() {
		t.Fatal("single-file torrent round trip changed infohash")
	}
	for _, format := range []string{"oss", "torrent", "cas"} {
		if diags := DiagnoseConversion(parsed, format); len(diags) > 0 {
			t.Fatalf("%s should be convertible, got %v", format, diags)
		}
	}
}
