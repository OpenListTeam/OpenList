package fs

import (
	"bytes"
	"io"
	"reflect"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
)

// 复现：按生成链路编码 torrent，再走解析链路，检查是否还能读回。
func TestReproGeneratedSeedRoundTrip(t *testing.T) {
	const piece = int64(1 << 20)
	seed := torrent.NewSeed("books", "OpenList", piece)
	seed.CreatedAt = time.Unix(1, 0).UTC().Format(time.RFC3339)
	global := torrent.NewHashWriter(piece, piece, 0)
	for _, fixture := range []struct {
		path string
		data []byte
	}{
		{"a.pdf", bytes.Repeat([]byte("a"), int(piece)+123)},
		{"b.pdf", bytes.Repeat([]byte("b"), int(piece)+456)},
	} {
		hasher := torrent.NewHashWriter(piece, piece, 0)
		if n, err := io.Copy(io.MultiWriter(global, hasher), bytes.NewReader(fixture.data)); err != nil || n != int64(len(fixture.data)) {
			t.Fatalf("hash %s: read %d bytes, error = %v", fixture.path, n, err)
		}
		hasher.Finish()
		file := hasher.BuildSeedFile(fixture.path, "")
		file.Hashes.GCID = ""
		seed.Files = append(seed.Files, file)
	}
	global.Finish()
	pieces := global.GetPieceHashes()
	data, err := EncodeGeneratedSeed(seed, "torrent", pieces)
	if err != nil {
		t.Fatalf("EncodeGeneratedSeed: %v", err)
	}
	parsed, err := torrent.DecodeSeed(data, "torrent", torrent.DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeSeed: %v", err)
	}
	if len(parsed.Files) != len(seed.Files) {
		t.Fatalf("parsed files = %d, want %d", len(parsed.Files), len(seed.Files))
	}
	for i, file := range parsed.Files {
		want := seed.Files[i]
		if file.Path != want.Path || file.Size != want.Size {
			t.Fatalf("file %d identity: got path=%q size=%d, want path=%q size=%d", i, file.Path, file.Size, want.Path, want.Size)
		}
		for _, hash := range []struct {
			name string
			got  string
			want string
		}{
			{"MD5", file.Hashes.MD5, want.Hashes.MD5},
			{"SHA1", file.Hashes.SHA1, want.Hashes.SHA1},
			{"SHA256", file.Hashes.SHA256, want.Hashes.SHA256},
		} {
			if hash.got != hash.want {
				t.Fatalf("file %d whole %s: got=%q, want=%q", i, hash.name, hash.got, hash.want)
			}
		}
		if !reflect.DeepEqual(file.Hashes.Pieces, want.Hashes.Pieces) {
			t.Fatalf("file %d pieces:\ngot=%#v\nwant=%#v", i, file.Hashes.Pieces, want.Hashes.Pieces)
		}
	}
	if !reflect.DeepEqual(parsed.GlobalPieces, global.GetPieceSHA1s()) {
		t.Fatalf("global SHA1 pieces:\ngot=%#v\nwant=%#v", parsed.GlobalPieces, global.GetPieceSHA1s())
	}
	original, err := torrent.Decode(data)
	if err != nil {
		t.Fatalf("Decode generated torrent: %v", err)
	}
	if !bytes.Equal(original.Info.Pieces, pieces) {
		t.Fatal("generated torrent pieces differ from the source-byte hashes")
	}
	for _, format := range []string{"oss", "torrent", "cas"} {
		t.Run(format, func(t *testing.T) {
			if diagnostics := torrent.DiagnoseConversion(parsed, format); len(diagnostics) != 0 {
				t.Fatalf("conversion diagnostics: %v", diagnostics)
			}
			encoded, err := torrent.EncodeSeed(parsed, format)
			if err != nil {
				t.Fatalf("EncodeSeed: %v", err)
			}
			decoded, err := torrent.DecodeSeed(encoded, format, torrent.DefaultParseLimits())
			if err != nil {
				t.Fatalf("DecodeSeed: %v", err)
			}
			if len(decoded.Files) != len(seed.Files) {
				t.Fatalf("files = %d, want %d", len(decoded.Files), len(seed.Files))
			}
			for i, file := range decoded.Files {
				if file.Path != seed.Files[i].Path || file.Size != seed.Files[i].Size || file.Hashes.MD5 != seed.Files[i].Hashes.MD5 {
					t.Fatalf("conversion changed file %d identity or whole MD5", i)
				}
			}
			if format != "cas" && !reflect.DeepEqual(decoded.GlobalPieces, global.GetPieceSHA1s()) {
				t.Fatal("conversion lost or changed whole-package pieces")
			}
			if format == "torrent" {
				rebuilt, err := torrent.Decode(encoded)
				if err != nil {
					t.Fatalf("Decode rebuilt torrent: %v", err)
				}
				if !bytes.Equal(rebuilt.Info.Pieces, pieces) || rebuilt.GetInfoHashHex() != original.GetInfoHashHex() {
					t.Fatal("torrent roundtrip changed pieces bytes or info hash")
				}
			}
		})
	}
}
