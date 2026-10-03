package task

import "testing"

func TestMatchTaskPath(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		dst    string
		prefix string
		want   bool
	}{
		{name: "dst equal", src: "", dst: "/a/b", prefix: "/a/b", want: true},
		{name: "dst child", src: "", dst: "/a/b/c", prefix: "/a/b", want: true},
		{name: "src child", src: "/a/b/c", dst: "/other", prefix: "/a/b", want: true},
		{name: "no false prefix", src: "", dst: "/ab", prefix: "/a", want: false},
		{name: "unrelated", src: "/x", dst: "/y", prefix: "/a", want: false},
		{name: "empty sides", src: "", dst: "", prefix: "/a", want: false},
		{name: "root matches all", src: "/a", dst: "", prefix: "/", want: true},
		{name: "backslash cleaned", src: "", dst: "\\a\\b\\c", prefix: "/a/b", want: true},
		{name: "relative cleaned", src: "", dst: "a/b/c", prefix: "/a/b", want: true},
		{name: "only src empty ignored", src: "", dst: "/keep/me", prefix: "/other", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchTaskPath(tt.src, tt.dst, tt.prefix); got != tt.want {
				t.Fatalf("MatchTaskPath(%q,%q,%q)=%v want %v", tt.src, tt.dst, tt.prefix, got, tt.want)
			}
		})
	}
}
