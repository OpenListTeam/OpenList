package crypt

import "testing"

func TestIsThumbPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		{path: "/photos/.thumbnails/cat.jpg.webp", want: true},
		{path: "/.thumbnails/cat.jpg.webp", want: true},
		{path: "/photos/cat.jpg.webp", want: false},
		{path: "/photos/.thumbnails/cat.jpg.png", want: false},
		{path: "/photos/.thumbnails/", want: false},
	}

	for _, tc := range cases {
		if got := isThumbPath(tc.path); got != tc.want {
			t.Fatalf("isThumbPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestThumbSourcePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{path: "/photos/.thumbnails/cat.jpg.webp", want: "/photos/cat.jpg", ok: true},
		{path: "/.thumbnails/cat.jpg.webp", want: "/cat.jpg", ok: true},
		{path: "/photos/.thumbnails/cat.jpg.png", ok: false},
		{path: "/photos/cat.jpg.webp", ok: false},
	}

	for _, tc := range cases {
		got, ok := thumbSourcePath(tc.path)
		if ok != tc.ok {
			t.Fatalf("thumbSourcePath(%q) ok = %v, want %v", tc.path, ok, tc.ok)
		}
		if got != tc.want {
			t.Fatalf("thumbSourcePath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestThumbTargetDir(t *testing.T) {
	t.Parallel()

	if got := thumbTargetDir("/photos/.thumbnails/cat.jpg.webp"); got != "/photos/.thumbnails" {
		t.Fatalf("thumbTargetDir() = %q, want %q", got, "/photos/.thumbnails")
	}
}
