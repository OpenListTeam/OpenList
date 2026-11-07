package handles

import (
	"os"
	"testing"
)

// TestMain removes the package data directory that init() creates, so a test
// run does not leave it behind in the system temp dir.
func TestMain(m *testing.M) {
	code := m.Run()
	if handlesTestDataDir != "" {
		_ = os.RemoveAll(handlesTestDataDir)
	}
	os.Exit(code)
}
