package scheduler

import "testing"

func TestEscapeUnescapeRoundTrip(t *testing.T) {
	cases := []string{
		"",
		"plain",
		"has:colon",
		"has\\backslash",
		"both:colon\\and\\backslash",
		"trailing\\",
		"nested\\:colon",
		"multiple::colons\\\\and\\mixed",
	}

	for _, tc := range cases {
		escaped := escapeTagStr(tc)
		got := unescapeTagStr(escaped)
		if got != tc {
			t.Fatalf("round trip failed for %q: got %q", tc, got)
		}
	}
}

func TestEscapeTagStrOutput(t *testing.T) {
	input := "k:e\\y"
	expected := "k\\:e\\\\y"
	if got := escapeTagStr(input); got != expected {
		t.Fatalf("escapeTagStr(%q)=%q, want %q", input, got, expected)
	}
}

func TestSplitEscapedTag(t *testing.T) {
	build := func(k, v string) string {
		return escapeTagStr(k) + ":" + escapeTagStr(v)
	}

	cases := []struct {
		name string
		tag  string
		key  string
		val  string
		ok   bool
	}{
		{name: "simple", tag: build("k", "v"), key: "k", val: "v", ok: true},
		{name: "escaped colon in key", tag: build("k:e:y", "val"), key: "k:e:y", val: "val", ok: true},
		{name: "escaped colon in val", tag: build("key", "v:a:l"), key: "key", val: "v:a:l", ok: true},
		{name: "backslash in both", tag: build("k\\ey", "v\\al"), key: "k\\ey", val: "v\\al", ok: true},
		{name: "no colon", tag: "nocolon", ok: false},
		{name: "only escaped colon", tag: "key\\:part", ok: false},
	}

	for _, tc := range cases {
		keyPart, valPart, ok := splitEscapedTag(tc.tag)
		if ok != tc.ok {
			t.Fatalf("%s: ok=%v, want %v (tag=%q)", tc.name, ok, tc.ok, tc.tag)
		}
		if !ok {
			continue
		}
		if keyPart != escapeTagStr(tc.key) {
			t.Fatalf("%s: key=%q, want %q", tc.name, keyPart, escapeTagStr(tc.key))
		}
		if valPart != escapeTagStr(tc.val) {
			t.Fatalf("%s: val=%q, want %q", tc.name, valPart, escapeTagStr(tc.val))
		}

		// Ensure unescape recovers originals.
		if unescapeTagStr(keyPart) != tc.key || unescapeTagStr(valPart) != tc.val {
			t.Fatalf("%s: unescape mismatch key=%q val=%q", tc.name, unescapeTagStr(keyPart), unescapeTagStr(valPart))
		}
	}
}
