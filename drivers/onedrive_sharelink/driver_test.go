package onedrive_sharelink

import "testing"

func TestRelativePath(t *testing.T) {
	root := "/personal/user/Documents/sub"
	tests := []struct {
		name        string
		root        string
		virtualPath string
		want        string
		wantErr     bool
	}{
		{"empty root", "", "/a/b", "/a/b", false},
		{"slash root", "/", "/a/b", "/a/b", false},
		{"exact root", root, root, "/", false},
		{"child", root, root + "/a", "/a", false},
		{"grandchild", root, root + "/a/b/c", "/a/b/c", false},
		{"dirty child", root, root + "/a/../b", "/b", false},
		{"non-english library", "/personal/user/文档/sub", "/personal/user/文档/sub/a", "/a", false},
		{"prefix collision", root, root + "A/x", "", true},
		{"outside root", root, "/other", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &OnedriveSharelink{}
			d.RootFolderPath = tt.root
			got, err := d.relativePath(tt.virtualPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("relativePath(%q) error = %v, wantErr %v", tt.virtualPath, err, tt.wantErr)
			}
			if err == nil && got != tt.want {
				t.Errorf("relativePath(%q) = %q, want %q", tt.virtualPath, got, tt.want)
			}
		})
	}
}

func TestEffectiveDriveRootPath(t *testing.T) {
	list := "/personal/user/Documents"
	driveRoot := "/"
	tests := []struct {
		name string
		root string
		list string
		want string
	}{
		{"no listURL", list, "", driveRoot},
		{"empty root", "", list, driveRoot},
		{"slash root", "/", list, driveRoot},
		{"exact list", list, list, "/"},
		{"child", list + "/sub", list, "/sub"},
		{"nested child", list + "/sub/a", list, "/sub/a"},
		{"prefix collision", list + "A", list, driveRoot},
		{"outside list", "/other", list, driveRoot},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &OnedriveSharelink{}
			d.RootFolderPath = tt.root
			d.listURL = tt.list
			d.driveRootPath = driveRoot
			if got := d.effectiveDriveRootPath(); got != tt.want {
				t.Errorf("effectiveDriveRootPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDrivePathAPIURL(t *testing.T) {
	const drive = "https://d.example.com"
	tests := []struct {
		name string
		root string
		list string
		path string
		want string
	}{
		{"no root, drive root", "", "", "/", drive + "/root"},
		{"no root, child", "", "", "/a", drive + "/root:/a:"},
		{"root equals list", "/personal/user/Documents", "/personal/user/Documents", "/a", drive + "/root:/a:"},
		{"root under list, child", "/personal/user/Documents/sub", "/personal/user/Documents", "/a", drive + "/root:/sub/a:"},
		{"custom library, child", "/sites/x/Shared Documents/sub", "/sites/x/Shared Documents", "/a", drive + "/root:/sub/a:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &OnedriveSharelink{}
			d.DriveURL = drive
			d.RootFolderPath = tt.root
			d.listURL = tt.list
			d.driveRootPath = "/"
			if got := d.drivePathAPIURL(tt.path); got != tt.want {
				t.Errorf("drivePathAPIURL(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
