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

func TestParseSize(t *testing.T) {
	testCases := []struct {
		input       string
		expected    int64
		expectError bool
	}{
		{"", 0, false},
		{"   ", 0, false},
		{"0", 0, false},
		{"100", 100, false},
		{"500B", 500, false},
		{"10Kb", 10 * 1024, false},
		{"10KB", 10 * 1024, false},
		{"10KiB", 10 * 1024, false},
		{"100MB", 100 * 1024 * 1024, false},
		{"100mb", 100 * 1024 * 1024, false},
		{"10G", 10 * 1024 * 1024 * 1024, false},
		{"10GB", 10 * 1024 * 1024 * 1024, false},
		{"1.5GB", int64(1.5 * 1024 * 1024 * 1024), false},
		{"1TB", 1 * 1024 * 1024 * 1024 * 1024, false},
		{"  100 MB  ", 100 * 1024 * 1024, false},
		{"invalid", 0, true},
		{"100XYZ", 0, true},
		{"-50MB", 0, true},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			res, err := ParseSize(tc.input)
			if tc.expectError {
				if err == nil {
					t.Errorf("ParseSize(%q) expected error, got nil", tc.input)
				}
			} else {
				if err != nil {
					t.Errorf("ParseSize(%q) unexpected error: %v", tc.input, err)
				}
				if res != tc.expected {
					t.Errorf("ParseSize(%q) = %d, want %d", tc.input, res, tc.expected)
				}
			}
		})
	}
}
