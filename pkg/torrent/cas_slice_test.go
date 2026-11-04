package torrent

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// test189PartSize 是 drivers/189pc 上传链路 partSize 的逐字副本，只用于对照，
// 保证 CAS 的分片大小与 189 实际使用的分片大小完全一致（不允许有第二种解释）。
func test189PartSize(size int64) int64 {
	const DEFAULT = 1024 * 1024 * 10 // 10MiB
	if size > DEFAULT*2*999 {
		return int64(math.Max(math.Ceil((float64(size)/1999)/float64(DEFAULT)), 5) * DEFAULT)
	}
	if size > DEFAULT*999 {
		return DEFAULT * 2 // 20MiB
	}
	return DEFAULT
}

// TestCASSliceSizeMatches189UploadRule 分片大小必须与 189 上传完全一致：
// 同一份字节在上传时按 partSize 切片，秒传时按 CASSliceSize 提交，
// 两者只要有一个边界不同，sliceMd5 就对不上云端。
func TestCASSliceSizeMatches189UploadRule(t *testing.T) {
	const tenMiB = 10 * 1024 * 1024
	sizes := []int64{
		0, 1, tenMiB - 1, tenMiB, tenMiB + 1,
		tenMiB*999 - 1, tenMiB * 999, tenMiB*999 + 1,
		tenMiB*2*999 - 1, tenMiB * 2 * 999, tenMiB*2*999 + 1,
		50 << 30, 100 << 30, 1 << 40, 4 << 40,
	}
	for _, size := range sizes {
		if got, want := CASSliceSize(size), test189PartSize(size); got != want {
			t.Fatalf("CASSliceSize(%d) = %d, 189 partSize = %d", size, got, want)
		}
	}
}

// TestCASPartCountMatches189UploadLoop 分片数量与 189 上传循环一致：
// size <= sliceSize 时只有 1 片，其余为 ceil(size/sliceSize)。
func TestCASPartCountMatches189UploadLoop(t *testing.T) {
	const tenMiB = 10 * 1024 * 1024
	for _, size := range []int64{1, tenMiB - 1, tenMiB, tenMiB + 1, tenMiB*999 + 1, 3 * tenMiB, 100 << 30} {
		sliceSize := test189PartSize(size)
		want := int64(1)
		if size > sliceSize {
			want = (size + sliceSize - 1) / sliceSize
		}
		if got := CASPartCount(size); got != want {
			t.Fatalf("CASPartCount(%d) = %d, 189 upload loop = %d", size, got, want)
		}
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
