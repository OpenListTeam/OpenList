package local

import "path/filepath"

// LocalPath implements driver.LocalPathProvider.
func (d *Local) LocalPath(actualPath string) string {
	return filepath.Join(d.GetRootPath(), filepath.FromSlash(actualPath))
}
