package imgmime

import "testing"

func TestSniff(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0}, "image/jpeg"},
		{"png", []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0}, "image/png"},
		{"gif89", []byte("GIF89a....."), "image/gif"},
		{"webp", append(append([]byte("RIFF\x00\x00\x00\x00"), []byte("WEBP")...), 0), "image/webp"},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg" onload="alert(1)"></svg>`), ""},
		{"svg with xml prolog", []byte(`<?xml version="1.0"?><svg/>`), ""},
		{"html", []byte("<html><script>alert(1)</script></html>"), ""},
		{"empty", nil, ""},
		{"short", []byte{0xFF, 0xD8}, ""},
		// RIFF 但不是 WEBP（例如 WAV）不能被当作图片
		{"riff wav", append(append([]byte("RIFF\x00\x00\x00\x00"), []byte("WAVE")...), 0), ""},
	}
	for _, tc := range cases {
		if got := Sniff(tc.data); got != tc.want {
			t.Errorf("%s: Sniff = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAllowed(t *testing.T) {
	for _, m := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
		if !Allowed(m) {
			t.Errorf("%s should be allowed", m)
		}
	}
	for _, m := range []string{"image/svg+xml", "text/html", "", "image/"} {
		if Allowed(m) {
			t.Errorf("%q should not be allowed", m)
		}
	}
}
