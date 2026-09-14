package http_range

import (
	"net/http"
	"testing"
)

func TestApplyRangeToHttpHeader(t *testing.T) {
	testcases := []struct {
		name   string
		r      Range
		expect string // expected Range header value, "" means absent
	}{
		{"whole file", Range{Start: 0, Length: -1}, ""},
		{"zero-length at start", Range{Start: 0, Length: 0}, ""},
		{"zero-length mid file", Range{Start: 100, Length: 0}, ""},
		{"normal range", Range{Start: 0, Length: 100}, "bytes=0-99"},
		{"offset range", Range{Start: 100, Length: 50}, "bytes=100-149"},
		{"open-ended range", Range{Start: 100, Length: -1}, "bytes=100-"},
		{"single byte", Range{Start: 5, Length: 1}, "bytes=5-5"},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			header := ApplyRangeToHttpHeader(tc.r, nil)
			got := header.Get("Range")
			if got != tc.expect {
				t.Errorf("ApplyRangeToHttpHeader(%+v) = %q, want %q", tc.r, got, tc.expect)
			}
		})
	}

	t.Run("keeps existing headers", func(t *testing.T) {
		header := http.Header{}
		header.Set("Accept", "*/*")
		out := ApplyRangeToHttpHeader(Range{Start: 0, Length: 0}, header)
		if out.Get("Range") != "" {
			t.Errorf("Range header should be absent, got %q", out.Get("Range"))
		}
		if out.Get("Accept") != "*/*" {
			t.Error("existing headers should be preserved")
		}
	})
}
