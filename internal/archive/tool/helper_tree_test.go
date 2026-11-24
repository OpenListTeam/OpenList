package tool

import (
	"encoding/json"
	"io"
	iofs "io/fs"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type fakeFileInfo struct {
	name string
	dir  bool
}

func (f fakeFileInfo) Name() string        { return f.name }
func (f fakeFileInfo) Size() int64         { return 0 }
func (f fakeFileInfo) Mode() iofs.FileMode { return 0 }
func (f fakeFileInfo) ModTime() time.Time  { return time.Unix(0, 0) }
func (f fakeFileInfo) IsDir() bool         { return f.dir }
func (f fakeFileInfo) Sys() any            { return nil }

type fakeSubFile struct {
	name string
	dir  bool
}

func (f fakeSubFile) Name() string                 { return f.name }
func (f fakeSubFile) FileInfo() iofs.FileInfo      { return fakeFileInfo{f.name, f.dir} }
func (f fakeSubFile) Open() (io.ReadCloser, error) { return nil, nil }

type fakeReader struct{ files []SubFile }

func (r fakeReader) Files() []SubFile { return r.files }

func TestGenerateMetaTreeNestedWithoutDirEntries(t *testing.T) {
	r := fakeReader{files: []SubFile{
		fakeSubFile{name: "top/README.txt"},
		fakeSubFile{name: "top/SHA256SUMS"},
		fakeSubFile{name: "top/sub/deep/file.txt"},
		fakeSubFile{name: "top/sub/other.txt"},
		fakeSubFile{name: "rootfile.txt"},
	}}
	_, tree := GenerateMetaTreeFromFolderTraversal(r)
	if _, err := json.Marshal(tree); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(tree) != 2 {
		t.Fatalf("root children = %d, want 2: %s", len(tree), names(tree))
	}
	found := false
	for _, n := range tree {
		if n.GetName() == "top" {
			found = true
			kids := n.GetChildren()
			if len(kids) != 3 {
				t.Fatalf("top children = %d, want 3: %s", len(kids), names(kids))
			}
			for _, k := range kids {
				if k.GetName() == "sub" && len(k.GetChildren()) != 2 {
					t.Fatalf("sub children = %d, want 2: %s", len(k.GetChildren()), names(k.GetChildren()))
				}
			}
		}
	}
	if !found {
		t.Fatalf("top folder missing: %s", names(tree))
	}
}

func names(ns []model.ObjTree) []string {
	out := make([]string, 0, len(ns))
	for _, n := range ns {
		out = append(out, n.GetName())
	}
	return out
}
