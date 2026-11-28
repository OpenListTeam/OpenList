package transmission

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hekmon/transmissionrpc/v3"
)

func TestSizeWhenDoneBytes(t *testing.T) {
	if got := sizeWhenDoneBytes(nil); got != 0 {
		t.Fatalf("nil torrent = %d, want 0", got)
	}
	for _, n := range []int64{0, 1, 8, 1048576, 5 << 30} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			raw := fmt.Sprintf(`{"sizeWhenDone":%d}`, n)
			var torrent transmissionrpc.Torrent
			if err := json.Unmarshal([]byte(raw), &torrent); err != nil {
				t.Fatal(err)
			}
			if got := sizeWhenDoneBytes(&torrent); got != n {
				t.Fatalf("sizeWhenDone %d decoded to %d bytes", n, got)
			}
		})
	}
}
