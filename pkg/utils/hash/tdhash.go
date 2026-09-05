package hash_extend

import (
	"hash"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/zeebo/blake3"
)

// tdBlockSize is TelDrive's fixed tree-hash block size.
const tdBlockSize = 16 * 1024 * 1024

// BLAKE3Tree is the content hash used by TelDrive v2.
//
// It is not plain BLAKE3: the stream is cut into fixed 16 MiB blocks, each block
// is hashed with BLAKE3, and the concatenated block digests are hashed once more
// to give the root. The server computes it for every upload part and reports it
// on every file, which is why it gets its own type rather than reusing "blake3"
// - a value produced by a plain BLAKE3 of the same bytes would not match.
//
// Algorithm mirrors teldrive's internal/treehash and rclone's
// backend/teldrive/tdhash (both MIT). rclone exposes the same value under the
// name "teldrive", so `rclone lsjson --hash` output is directly comparable.
var BLAKE3Tree = utils.RegisterHash("blake3_tree", "BLAKE3-Tree", 64, NewBLAKE3Tree)

func NewBLAKE3Tree() hash.Hash {
	return &blake3Tree{block: blake3.New()}
}

type blake3Tree struct {
	block  *blake3.Hasher
	blocks [][]byte // digests of the completed blocks
	n      int      // bytes written into the current block
}

func (h *blake3Tree) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		room := tdBlockSize - h.n
		if room > len(p) {
			room = len(p)
		}
		h.block.Write(p[:room])
		h.n += room
		p = p[room:]

		if h.n == tdBlockSize {
			h.blocks = append(h.blocks, h.block.Sum(nil))
			h.block.Reset()
			h.n = 0
		}
	}
	return n, nil
}

// Sum does not consume the hasher: a trailing partial block is folded into a
// copy so that writing can continue afterwards.
func (h *blake3Tree) Sum(b []byte) []byte {
	digests := h.blocks
	if h.n > 0 {
		digests = append(append(make([][]byte, 0, len(h.blocks)+1), h.blocks...), h.block.Sum(nil))
	}
	root := blake3.New()
	for _, d := range digests {
		root.Write(d)
	}
	return root.Sum(b)
}

func (h *blake3Tree) Reset() {
	h.block.Reset()
	h.blocks = nil
	h.n = 0
}

func (h *blake3Tree) Size() int { return 32 }

func (h *blake3Tree) BlockSize() int { return tdBlockSize }
