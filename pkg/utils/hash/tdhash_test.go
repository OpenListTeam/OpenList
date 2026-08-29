package hash_extend

import (
	"encoding/hex"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// fixture reproduces the byte pattern of the file these vectors were captured
// from: a 36 MiB stream where data[i] == byte(i % 256). off is the offset of the
// returned slice within that stream.
func fixture(off, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((off + i) % 256)
	}
	return b
}

// Vectors captured from a live teldrive v2 server (1.8.3-SNAPSHOT-e3142b5) by
// uploading the fixture in 16 MiB parts and recording the checksum the server
// computed for each part plus the tree hash it published for the whole file.
// 36 MiB is deliberate: two whole 16 MiB blocks plus a 4 MiB remainder, so the
// vectors cover a full block, a partial block, and the concatenation of both.
func TestBLAKE3TreeServerVectors(t *testing.T) {
	for _, tc := range []struct {
		name string
		off  int
		n    int
		want string
	}{
		{"whole file: 2 full blocks + partial", 0, 37748736, "c2ccef62af6cd438401c9a547d031c01e07b066483420321265acde683a414f3"},
		{"part 1: exactly one block", 0, 16777216, "0d6739b729971512b1d02c2029eae45525db27ddcd5fdfb5010256a6c946c674"},
		{"part 3: partial block", 33554432, 4194304, "7cf2f855eaf2fc6863e64ce56879f61e3d91eb0e02ff0ae28f713807e3bd2ab6"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := utils.HashData(BLAKE3Tree, fixture(tc.off, tc.n)); got != tc.want {
				t.Errorf("hash = %s, want %s", got, tc.want)
			}
		})
	}
}

// The block boundary is the only interesting state in Write, so feed the same
// bytes in sizes that straddle it and confirm the result never changes.
func TestBLAKE3TreeIsWriteChunkingIndependent(t *testing.T) {
	data := fixture(0, tdBlockSize+4096)
	want := utils.HashData(BLAKE3Tree, data)

	for _, step := range []int{1, 7, 4096, tdBlockSize - 1, tdBlockSize, tdBlockSize + 1} {
		h := NewBLAKE3Tree()
		for off := 0; off < len(data); off += step {
			end := min(off+step, len(data))
			if _, err := h.Write(data[off:end]); err != nil {
				t.Fatalf("Write: %v", err)
			}
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != want {
			t.Errorf("step %d: hash = %s, want %s", step, got, want)
		}
	}
}

// Sum must not consume the hasher: the driver hashes a part, then hashes the
// next one with a fresh hasher, and utils.HashReader calls Sum once at the end.
func TestBLAKE3TreeSumIsRepeatable(t *testing.T) {
	h := NewBLAKE3Tree()
	h.Write(fixture(0, 1000))

	first := hex.EncodeToString(h.Sum(nil))
	if second := hex.EncodeToString(h.Sum(nil)); second != first {
		t.Errorf("Sum is destructive: %s then %s", first, second)
	}

	h.Write(fixture(1000, 1000))
	if after := hex.EncodeToString(h.Sum(nil)); after == first {
		t.Error("Sum ignored data written after an earlier Sum")
	}
}

func TestBLAKE3TreeRegistered(t *testing.T) {
	ht, ok := utils.GetHashByName("blake3_tree")
	if !ok {
		t.Fatal("blake3_tree is not registered")
	}
	if ht != BLAKE3Tree {
		t.Error("blake3_tree resolves to a different hash type")
	}
	if BLAKE3Tree.Width != 64 {
		t.Errorf("Width = %d, want 64", BLAKE3Tree.Width)
	}
}
