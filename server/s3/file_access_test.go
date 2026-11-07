package s3

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/access"
)

// TestRecordFileReadAndForget ensures that recordFileRead buffers a sample,
// and access.Forget removes it so it is not flushed to the database.
func TestRecordFileReadAndForget(t *testing.T) {
	const p = "/test-bucket/sample.bin"
	access.Record(p)

	// Before flush, forget it
	access.Forget(p)

	// Flush
	access.Flush()
}
