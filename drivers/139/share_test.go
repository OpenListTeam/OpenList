package _139

import (
	"testing"
)

func TestShareEntriesAndRefEncoding(t *testing.T) {
	d := &Yun139{Addition: Addition{
		Type:   MetaShare,
		LinkID: "share-a,share-b,share-c#pass",
	}}

	entries := d.shareEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 share entries, got %d", len(entries))
	}
	if entries[2].LinkID != "share-c" || entries[2].Password != "pass" {
		t.Fatalf("unexpected password share entry: %+v", entries[2])
	}

	refs := []shareRef{
		{LinkID: entries[0].LinkID, Password: entries[0].Password, NodeID: "root-a"},
		{LinkID: entries[2].LinkID, Password: entries[2].Password, NodeID: "root-b"},
	}
	encoded := encodeShareRefs(refs)
	decoded, ok := decodeShareRefs(encoded)
	if !ok || len(decoded) != len(refs) {
		t.Fatalf("failed to decode merged share refs: %q", encoded)
	}
	for i := range refs {
		if decoded[i] != refs[i] {
			t.Fatalf("unexpected decoded ref at %d: %+v", i, decoded[i])
		}
	}
}
