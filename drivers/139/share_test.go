package _139

import (
	"testing"
)

func TestShareEntriesAndRefEncoding(t *testing.T) {
	d := &Yun139{Addition: Addition{
		Type:   MetaShare,
		LinkID: "2w2KLTrz2Y8bd,2uR1zFho3YNcj,2w2KLzpSwt57p#f95e",
	}}

	entries := d.shareEntries()
	if len(entries) != 3 {
		t.Fatalf("expected 3 share entries, got %d", len(entries))
	}
	if entries[2].LinkID != "2w2KLzpSwt57p" || entries[2].Password != "f95e" {
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
