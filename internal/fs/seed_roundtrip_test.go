package fs

import (
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
)

// 复现：按生成链路编码 torrent，再走解析链路，检查是否还能读回。
func TestReproGeneratedSeedRoundTrip(t *testing.T) {
	const piece = int64(1 << 20)
	seed := torrent.NewSeed("books", "OpenList", piece)
	seed.CreatedAt = time.Unix(1, 0).UTC().Format(time.RFC3339)
	seed.Files = []torrent.SeedFile{
		{
			Path: "a.pdf",
			Size: piece + 123,
			Hashes: torrent.SeedHashes{
				MD5:    strings.Repeat("1", 32),
				SHA1:   strings.Repeat("2", 40),
				SHA256: strings.Repeat("3", 64),
				Pieces: &torrent.SeedPieceHashes{SHA1: []string{strings.Repeat("a", 40), strings.Repeat("b", 40)}},
			},
		},
		{
			Path: "b.pdf",
			Size: piece + 456,
			Hashes: torrent.SeedHashes{
				MD5:    strings.Repeat("4", 32),
				SHA1:   strings.Repeat("5", 40),
				SHA256: strings.Repeat("6", 64),
				Pieces: &torrent.SeedPieceHashes{SHA1: []string{strings.Repeat("c", 40), strings.Repeat("d", 40)}},
			},
		},
	}
	pieces := make([]byte, 0, 3*20)
	for i := 0; i < 3; i++ {
		for j := 0; j < 20; j++ {
			pieces = append(pieces, byte(i*20+j))
		}
	}
	data, err := EncodeGeneratedSeed(seed, "torrent", pieces)
	if err != nil {
		t.Fatalf("EncodeGeneratedSeed: %v", err)
	}
	parsed, err := torrent.DecodeSeed(data, "torrent", torrent.DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeSeed: %v", err)
	}
	t.Logf("parsed files=%d available=%v/%v", len(parsed.Files), parsed.AvailableHashes, parsed.AvailablePieceHashes)
	for _, format := range []string{"oss", "torrent", "cas"} {
		t.Logf("%s -> %v", format, torrent.DiagnoseConversion(parsed, format))
	}
}
