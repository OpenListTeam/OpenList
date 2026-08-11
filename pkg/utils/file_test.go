package utils

import (
	"testing"
)

func TestIsSystemFile(t *testing.T) {
	testCases := []struct {
		filename string
		expected bool
	}{
		// System files that should be filtered
		{".DS_Store", true},
		{"desktop.ini", true},
		{"Thumbs.db", true},
		{"._test.txt", true},
		{"._", true},
		{"._somefile", true},
		{"._folder_name", true},
		{"@eaDir", true},

		// Regular files that should not be filtered
		{"test.txt", false},
		{"file.pdf", false},
		{"document.docx", false},
		{".gitignore", false},
		{".env", false},
		{"_underscore.txt", false},
		{"normal_file.txt", false},
		{"", false},
		{".hidden", false},
		{"..special", false},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			result := IsSystemFile(tc.filename)
			if result != tc.expected {
				t.Errorf("IsSystemFile(%q) = %v, want %v", tc.filename, result, tc.expected)
			}
		})
	}
}
