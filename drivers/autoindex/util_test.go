package autoindex

import (
	"testing"
)

func TestParseSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"100", 100},
		{"1k", 1024},
		{"1kb", 1024},
		{"1K", 1024},      // case insensitive
		{"1.5m", 1572864}, // 1.5 * 1024^2
		{"500 bytes", 500},
		{"-", 0},
		{"", 0},
		{"abc", 0},
		{"1.5GB", 1610612736},    // 1.5 * 1024^3
		{"2t", 2199023255552},    // 2 * 1024^4
		{"1p", 1125899906842624}, // 1 * 1024^5
		{"0", 0},
		{"  100  ", 100}, // trimmed
		{"100b", 100},
		{"1gib", 1073741824}, // 1024^3
		{"1z", 1},            // invalid unit, mul=1
		{"1.5", 1},           // float without unit, truncated
		{"2.7k", 2764},       // 2.7 * 1024 truncated
		{"1.0g", 1073741824}, // 1.0 * 1024^3
		{"invalid", 0},
		{"123xyz", 123}, // unit not found, mul=1
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, _ := parseSize(tt.input)
			if got != tt.want {
				t.Errorf("ParseSize(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}
