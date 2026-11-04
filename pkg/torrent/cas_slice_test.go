package torrent

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// TestCASSliceSizeMatches189Rule 天翼的分片大小只由文件大小决定：
// 10MiB / 20MiB，更大的文件按 1999 片上限向上取 10MiB 的整数倍（至少 5 倍）。
// 用户配置的 piece size 不参与这个判断。
func TestCASSliceSizeMatches189Rule(t *testing.T) {
	cases := []struct{ size, want int64 }{
		{0, DefaultPieceSize},
		{1, DefaultPieceSize},
		{DefaultPieceSize, DefaultPieceSize},
		{DefaultPieceSize*999 + 1, DefaultPieceSize * 2},
		{DefaultPieceSize * 2 * 999, DefaultPieceSize * 2},
		{DefaultPieceSize*2*999 + 1, DefaultPieceSize * 5},
	}
	for _, c := range cases {
		if got := CASSliceSize(c.size); got != c.want {
			t.Fatalf("CASSliceSize(%d) = %d, want %d", c.size, got, c.want)
		}
	}
	if got := CASPartCount(DefaultPieceSize + 1); got != 2 {
		t.Fatalf("CASPartCount(10MiB+1) = %d, want 2", got)
	}
}

// TestCASUniformSliceSize 种子级 piece_size 只能存一个切法；只有所有文件
// 落在同一分片档位时，逐片哈希才能挂到种子上。
func TestCASUniformSliceSize(t *testing.T) {
	small := SeedFile{Size: DefaultPieceSize}
	large := SeedFile{Size: DefaultPieceSize*999 + 1}

	if size, ok := CASUniformSliceSize([]SeedFile{small, small}); !ok || size != DefaultPieceSize {
		t.Fatalf("CASUniformSliceSize(small, small) = %d, %v", size, ok)
	}
	if _, ok := CASUniformSliceSize([]SeedFile{small, large}); ok {
		t.Fatal("files in different slice tiers must not report a uniform size")
	}
	if _, ok := CASUniformSliceSize(nil); ok {
		t.Fatal("empty file list must not report a uniform size")
	}
}

func decodeCASJSON(t *testing.T, encoded []byte) map[string]any {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	var payload map[string]any
	if err = json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	return payload
}

// TestEncodeCASStoresFinalSliceMD5AndParts 新格式把「最终摘要」写在 sliceMd5、
// 逐片 MD5 写在 partsMd5；旧字段 slice_md5s 不再写出。
func TestEncodeCASStoresFinalSliceMD5AndParts(t *testing.T) {
	seed := testSeed()
	encoded, err := EncodeCAS(seed)
	if err != nil {
		t.Fatalf("EncodeCAS() error = %v", err)
	}
	payload := decodeCASJSON(t, encoded)

	if _, exists := payload["slice_md5s"]; exists {
		t.Fatalf("new CAS payload should not write legacy slice_md5s: %#v", payload)
	}
	parts, ok := payload["partsMd5"].([]any)
	if !ok || len(parts) != 2 {
		t.Fatalf("partsMd5 = %#v, want 2 entries", payload["partsMd5"])
	}
	want := SliceMD5FromPieces([]string{
		strings.Repeat("4", 32), strings.Repeat("5", 32),
	}, strings.Repeat("1", 32))
	if payload["sliceMd5"] != want {
		t.Fatalf("sliceMd5 = %v, want %q", payload["sliceMd5"], want)
	}
}

// TestDecodeCASWithFinalSliceMD5Only 只有最终摘要的旧 .cas（五字段）必须能解析，
// 且不需要逐片列表；分片大小由文件大小推导。
func TestDecodeCASWithFinalSliceMD5Only(t *testing.T) {
	const (
		fileMD5   = "8DDEF8D30B87D9421CE1AC41C2A25BA3"
		sliceMD5  = "07954EF61C1F1A512E5ADC3B0352BE07"
		fileSize  = int64(3047921944)
		rawString = `{"name":"a.mp4","size":3047921944,"md5":"8DDEF8D30B87D9421CE1AC41C2A25BA3","sliceMd5":"07954EF61C1F1A512E5ADC3B0352BE07","create_time":"1793774205"}`
	)
	seed, err := DecodeCAS([]byte(rawString), DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeCAS() error = %v", err)
	}
	file := seed.Files[0]
	if file.CASSliceMD5 != strings.ToLower(sliceMD5) {
		t.Fatalf("CASSliceMD5 = %q, want %q", file.CASSliceMD5, strings.ToLower(sliceMD5))
	}
	if file.Hashes.Pieces != nil {
		t.Fatalf("payload without partsMd5 must not invent per-slice hashes: %#v", file.Hashes.Pieces)
	}
	if seed.PieceSize != CASSliceSize(fileSize) {
		t.Fatalf("piece size = %d, want %d", seed.PieceSize, CASSliceSize(fileSize))
	}

	// 重新编码后仍然只凭最终摘要就能带上判重值
	encoded, err := EncodeCAS(seed)
	if err != nil {
		t.Fatalf("EncodeCAS() error = %v", err)
	}
	payload := decodeCASJSON(t, encoded)
	if payload["sliceMd5"] != sliceMD5 {
		t.Fatalf("re-encoded sliceMd5 = %v, want %q", payload["sliceMd5"], sliceMD5)
	}
	if payload["md5"] != fileMD5 {
		t.Fatalf("re-encoded md5 = %v, want %q", payload["md5"], fileMD5)
	}
}

// TestDecodeCASAcceptsLegacySliceMD5s 旧字段 slice_md5s 仍作为逐片列表读入。
func TestDecodeCASAcceptsLegacySliceMD5s(t *testing.T) {
	fileMD5 := strings.Repeat("1", 32)
	parts := []string{strings.Repeat("a", 32), strings.Repeat("b", 32)}
	raw, err := json.Marshal(CASPayload{
		Name: "a.bin", Size: DefaultPieceSize + 1, MD5: fileMD5,
		SliceMD5:  SliceMD5FromPieces(parts, fileMD5),
		SliceMD5s: parts,
		SliceSize: DefaultPieceSize,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := DecodeCAS(raw, DefaultParseLimits())
	if err != nil {
		t.Fatalf("DecodeCAS() error = %v", err)
	}
	if seed.Files[0].Hashes.Pieces == nil || len(seed.Files[0].Hashes.Pieces.MD5) != 2 {
		t.Fatalf("legacy slice_md5s not restored: %#v", seed.Files[0].Hashes.Pieces)
	}
}
