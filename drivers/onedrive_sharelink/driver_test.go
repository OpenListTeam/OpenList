package onedrive_sharelink

import "testing"

func TestRelativePath(t *testing.T) {
	root := "/personal/user/Documents/sub"
	tests := []struct {
		name        string
		root        string
		virtualPath string
		want        string
	}{
		{"empty root", "", "/a/b", "/a/b"},
		{"slash root", "/", "/a/b", "/a/b"},
		{"exact root", root, root, "/"},
		{"child", root, root + "/a", "/a"},
		{"grandchild", root, root + "/a/b/c", "/a/b/c"},
		{"dirty child", root, root + "/a/../b", "/b"},
		{"prefix collision", root, root + "A/x", root + "A/x"},
		{"outside root", root, "/other", "/other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := &OnedriveSharelink{}
			d.RootFolderPath = tt.root
			if got := d.relativePath(tt.virtualPath); got != tt.want {
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
