package autoindex

import (
	"testing"
)

type wantType struct {
	v     int64
	exact bool
	error bool
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		input string
		want  wantType
	}{
		{"100", wantType{100, true, false}},
		{"1k", wantType{1024, false, false}},
		{"1kb", wantType{1024, false, false}},
		{"1K", wantType{1024, false, false}},      // case insensitive
		{"1.5m", wantType{1572864, false, false}}, // 1.5 * 1024^2
		{"500 bytes", wantType{500, true, false}},
		{"-", wantType{0, false, false}},
		{"", wantType{0, false, false}},
		{"abc", wantType{0, false, true}},
		{"1.5GB", wantType{1610612736, false, false}},    // 1.5 * 1024^3
		{"2t", wantType{2199023255552, false, false}},    // 2 * 1024^4
		{"1p", wantType{1125899906842624, false, false}}, // 1 * 1024^5
		{"0", wantType{0, true, false}},
		{"  100  ", wantType{100, true, false}}, // trimmed
		{"100b", wantType{100, true, false}},
		{"1gib", wantType{1073741824, false, false}}, // 1024^3
		{"1z", wantType{1, false, false}},            // invalid unit, mul=1
		{"1.5", wantType{1, false, false}},           // float without unit, truncated
		{"2.7k", wantType{2764, false, false}},       // 2.7 * 1024 truncated
		{"1.0g", wantType{1073741824, false, false}}, // 1.0 * 1024^3
		{"invalid", wantType{0, false, true}},
		{"123xyz", wantType{123, false, false}}, // unit not found, mul=1
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, exact, err := parseSize(tt.input)
			if got != tt.want.v || exact != tt.want.exact || (err != nil) != tt.want.error {
				t.Errorf("ParseSize(%q) = (%d, %t, %t), want (%d, %t, %t)", tt.input, got, exact, err != nil, tt.want.v, tt.want.exact, tt.want.error)
			}
		})
	}
}
