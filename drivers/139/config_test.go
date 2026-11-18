package _139

import "testing"

func TestConfigNoOverwriteRenameOnlyForPersonalNew(t *testing.T) {
	for typ, want := range map[string]bool{
		MetaPersonalNew: true,
		MetaPersonal:    false,
		MetaFamily:      false,
		MetaGroup:       false,
		"share":         false,
	} {
		d := &Yun139{Addition: Addition{Type: typ}}
		if got := d.Config().NoOverwriteRename; got != want {
			t.Errorf("type %q: NoOverwriteRename=%v, want %v", typ, got, want)
		}
	}
}
