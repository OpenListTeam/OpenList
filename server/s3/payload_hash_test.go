package s3

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/gofakes3"
	"github.com/stretchr/testify/require"
)

// A PUT whose body does not hash to the value the request declared must not
// leave anything behind. The check can only conclude at EOF, by which point the
// storage driver has written the bytes, so the write has to be undone - storing
// them would publish content nobody signed.
func TestPayloadHashMismatchLeavesNoObject(t *testing.T) {
	backend, localRoot := setupMultipartBackend(t)
	ctx := context.Background()

	body := "the bytes that arrived"
	sum := sha256.Sum256([]byte("the bytes that were signed"))
	guarded := &hashingBody{
		inner: io.NopCloser(strings.NewReader(body)),
		want:  hex.EncodeToString(sum[:]),
		sum:   sha256.New(),
	}

	_, err := backend.putStream(ctx, "mp", "sha/obj.txt", map[string]string{"Content-Type": "text/plain"},
		guarded, int64(len(body)))
	require.Error(t, err)
	require.True(t, gofakes3.HasErrorCode(err, gofakes3.ErrBadDigest),
		"a client-side payload mismatch must stay a 400, got %v", err)

	if _, statErr := os.Stat(filepath.Join(localRoot, "sha", "obj.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("the rejected body was left in the bucket (stat err = %v)", statErr)
	}
	if _, headErr := backend.HeadObject(ctx, "mp", "sha/obj.txt"); headErr == nil {
		t.Fatal("HEAD still finds the object that was rejected")
	}

	// The same call with a matching hash must store the object.
	good := &hashingBody{
		inner: io.NopCloser(strings.NewReader(body)),
		want:  func() string { s := sha256.Sum256([]byte(body)); return hex.EncodeToString(s[:]) }(),
		sum:   sha256.New(),
	}
	_, err = backend.putStream(ctx, "mp", "sha/obj.txt", map[string]string{"Content-Type": "text/plain"},
		good, int64(len(body)))
	require.NoError(t, err)
	info, err := backend.HeadObject(ctx, "mp", "sha/obj.txt")
	require.NoError(t, err)
	require.Equal(t, int64(len(body)), info.Size)
}
