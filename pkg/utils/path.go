package utils

import (
	"net/url"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
)

// FixAndCleanPath
// The upper layer of the root directory is still the root directory.
// So ".." And "." will be cleared
// for example
// 1. ".." or "." => "/"
// 2. "../..." or "./..." => "/..."
// 3. "../.x." or "./.x." => "/.x."
// 4. "x//\\y" = > "/z/x"
func FixAndCleanPath(path string) string {
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return stdpath.Clean(path)
}

// PathAddSeparatorSuffix Add path '/' suffix
// for example /root => /root/
func PathAddSeparatorSuffix(path string) string {
	if !strings.HasSuffix(path, "/") {
		path = path + "/"
	}
	return path
}

// PathEqual judge path is equal
func PathEqual(path1, path2 string) bool {
	return FixAndCleanPath(path1) == FixAndCleanPath(path2)
}

func IsSubPath(path string, subPath string) bool {
	path, subPath = FixAndCleanPath(path), FixAndCleanPath(subPath)
	return path == subPath || strings.HasPrefix(subPath, PathAddSeparatorSuffix(path))
}

func Ext(path string) string {
	ext := stdpath.Ext(path)
	if len(ext) > 0 && ext[0] == '.' {
		ext = ext[1:]
	}
	return strings.ToLower(ext)
}

func EncodePath(path string, all ...bool) string {
	seg := strings.Split(path, "/")
	toReplace := []struct {
		Src string
		Dst string
	}{
		{Src: "%", Dst: "%25"},
		{"%", "%25"},
		{"?", "%3F"},
		{"#", "%23"},
	}
	for i := range seg {
		if len(all) > 0 && all[0] {
			seg[i] = url.PathEscape(seg[i])
		} else {
			for j := range toReplace {
				seg[i] = strings.ReplaceAll(seg[i], toReplace[j].Src, toReplace[j].Dst)
			}
		}
	}
	return strings.Join(seg, "/")
}

func JoinBasePath(basePath, reqPath string) (string, error) {
	isRelativePath := strings.Contains(reqPath, "..")
	reqPath = FixAndCleanPath(reqPath)
	if isRelativePath && !strings.Contains(reqPath, "..") {
		return "", errs.RelativePath
	}
	return stdpath.Join(FixAndCleanPath(basePath), reqPath), nil
}

func GetFullPath(mountPath, path string) string {
	return stdpath.Join(GetActualMountPath(mountPath), path)
}

// GetPathHierarchy generates a hierarchy of paths from the given path.
//
// Example:
//  1. "/" => {"/"}
//  2. "" => {"/"}
//  3. "/a/b/c" => {"/", "/a", "/a/b", "/a/b/c"}
//  4. "/a/b/c/d/e.txt" => {"/", "/a", "/a/b", "/a/b/c", "/a/b/c/d", "/a/b/c/d/e.txt"}
//  5. "./a/b///c" => {"/", "/a", "/a/b", "/a/b/c"}
func GetPathHierarchy(path string) []string {
	if path == "" || path == "/" {
		return []string{"/"}
	}

	path = FixAndCleanPath(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	hierarchy := []string{"/"}

	parts := strings.Split(path, "/")
	currentPath := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		currentPath += "/" + part
		hierarchy = append(hierarchy, currentPath)
	}

	return hierarchy
}
