package smb

import "testing"

func TestStagingName(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		name := stagingName()
		if !isStaging(name) {
			t.Fatalf("generated staging name %q is not recognized as staging", name)
		}
		if seen[name] {
			t.Fatalf("staging name %q generated twice", name)
		}
		seen[name] = true
	}
}

// Only the exact generated form is an upload in progress: a user's own file
// that merely starts with the prefix must stay listed and never be reaped.
func TestListKeepsLookalikeUserFile(t *testing.T) {
	for _, name := range []string{
		".openlist-upload-notes.txt",
		".openlist-upload-short",
		".openlist-upload-AAAAAAAAAAAAAAAAA",
		".openlist-upload-AAAAAAAAAAAAAAA!",
		"openlist-upload-AAAAAAAAAAAAAAAA",
		"report.pdf",
	} {
		if isStaging(name) {
			t.Errorf("isStaging(%q) = true, want a user file", name)
		}
	}
	if !isStaging(".openlist-upload-AbCdEfGh01234567") {
		t.Error("a generated-form name is not recognized as staging")
	}
}
